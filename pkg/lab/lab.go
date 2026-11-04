// Copyright (c) 2026, Omer Sen.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package lab is what the MCP tools do, with no MCP in it. Reading works on the one
// cluster the configured kubeconfig points at. Changing anything is off unless the
// operator turns it on and needs a kind cluster, and injecting a fault also needs the
// DCGM pod to run only the simulated image, so a real GPU's hostengine is never written to.
package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	nvsentinelNamespace = "nvsentinel"
	dcgmNamespace       = "gpu-operator"
	dcgmSelector        = "app=nvidia-dcgm"
	dcgmService         = "nvidia-dcgm"
	dcgmOutageApp       = "nvidia-dcgm-outage"
	dcgmApp             = "nvidia-dcgm"
	fakeDCGMRepository  = "ghcr.io/nvidia/nvsentinel-fake-dcgm"
	workloadNamespace   = "default"
	workloadName        = "gpu-workload"
	workloadSelector    = "app=gpu-workload"
	gpuNodeSelector     = "nvidia.com/gpu.present=true"
	stateLabel          = "dgxc.nvidia.com/nvsentinel-state"
	connectivityCheck   = "GpuDcgmConnectivityFailure"
	kindProviderPrefix  = "kind://"

	// DCGM_FI_DEV_XID_ERRORS.
	xidField = "230"

	// XID95 is an uncontained ECC error: fatal, with a recommended action of RESTART_VM.
	XID95 = 95

	// MaxLogLines, MaxEvents and MaxWait bound what a tool can ask for.
	MaxLogLines = 2000
	MaxEvents   = 200
	MaxWait     = 900 * time.Second

	apiTimeout = 60 * time.Second
)

// Lab is one connection to one cluster.
type Lab struct {
	client       kubernetes.Interface
	execer       Execer
	context      string
	labContext   string
	allowChanges bool
	poll         time.Duration
}

// New builds a Lab from parts. Open is the way in for a real cluster.
func New(client kubernetes.Interface, execer Execer, contextName string, config Config) *Lab {
	return &Lab{
		client:       client,
		execer:       execer,
		context:      contextName,
		labContext:   config.LabContext,
		allowChanges: config.AllowChanges,
		poll:         3 * time.Second,
	}
}

// SetPoll changes how often WaitFor checks. Tests use it.
func (l *Lab) SetPoll(poll time.Duration) { l.poll = poll }

// RequireLab refuses unless changes are allowed and this is the kind lab, by the
// context's name and by every node's provider ID.
func (l *Lab) RequireLab(ctx context.Context) error {
	if !l.allowChanges {
		return failf("refusing: changes are off; the operator sets %s=true to allow them", EnvAllowChanges)
	}

	if err := validLabContext(l.labContext); err != nil {
		return failf("refusing: %v", err)
	}

	if l.context != l.labContext {
		return failf("refusing: the context is %q, not the lab's %q", l.context, l.labContext)
	}

	nodes, err := l.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return apiFailure("listing nodes", err)
	}

	if len(nodes.Items) == 0 {
		return failf("refusing: the cluster has no nodes")
	}

	for i := range nodes.Items {
		if !strings.HasPrefix(nodes.Items[i].Spec.ProviderID, kindProviderPrefix) {
			return failf("refusing: node %s is not a kind node", nodes.Items[i].Name)
		}
	}

	return nil
}

// isSimulated matches the simulated image's repository exactly, by tag or digest, so a
// look-alike such as nvsentinel-fake-dcgm-evil does not pass.
func isSimulated(image string) bool {
	return strings.HasPrefix(image, fakeDCGMRepository+":") || strings.HasPrefix(image, fakeDCGMRepository+"@")
}

func onlySimulatedImages(pod *corev1.Pod) bool {
	images := make([]string, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		images = append(images, pod.Spec.Containers[i].Image)
	}

	for i := range pod.Spec.InitContainers {
		images = append(images, pod.Spec.InitContainers[i].Image)
	}

	for i := range pod.Spec.EphemeralContainers {
		images = append(images, pod.Spec.EphemeralContainers[i].Image)
	}

	return len(images) > 0 && !slices.ContainsFunc(images, func(image string) bool { return !isSimulated(image) })
}

func (l *Lab) gpuNode(ctx context.Context) (*corev1.Node, error) {
	nodes, err := l.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: gpuNodeSelector})
	if err != nil {
		return nil, apiFailure("listing GPU nodes", err)
	}

	if len(nodes.Items) != 1 {
		return nil, failf("expected one node labelled %s, found %d", gpuNodeSelector, len(nodes.Items))
	}

	return &nodes.Items[0], nil
}

func (l *Lab) dcgmPods(ctx context.Context, node string) ([]corev1.Pod, error) {
	pods, err := l.client.CoreV1().Pods(dcgmNamespace).List(ctx, metav1.ListOptions{LabelSelector: dcgmSelector})
	if err != nil {
		return nil, apiFailure("listing DCGM pods", err)
	}

	live := make([]corev1.Pod, 0, len(pods.Items))

	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp == nil && pods.Items[i].Spec.NodeName == node {
			live = append(live, pods.Items[i])
		}
	}

	return live, nil
}

// fakeDCGMPod returns the node's DCGM pod, only if it runs nothing but the simulated image.
func (l *Lab) fakeDCGMPod(ctx context.Context, node string) (*corev1.Pod, error) {
	pods, err := l.dcgmPods(ctx, node)
	if err != nil {
		return nil, err
	}

	if len(pods) == 0 {
		return nil, failf("no DCGM pod on %s", node)
	}

	if !onlySimulatedImages(&pods[0]) {
		return nil, failf("refusing: the DCGM pod does not run only the simulated image %s", fakeDCGMRepository)
	}

	return &pods[0], nil
}

// NodeStatus is one node as NVSentinel sees it.
type NodeStatus struct {
	Name          string   `json:"name"`
	Ready         bool     `json:"ready"`
	Cordoned      bool     `json:"cordoned"`
	State         string   `json:"state,omitempty"`
	FailingChecks []string `json:"failingChecks"`
	ChecksTotal   int      `json:"checksReported"`
}

// PodStatus is one pod's health.
type PodStatus struct {
	Name     string `json:"name"`
	Node     string `json:"node,omitempty"`
	Phase    string `json:"phase"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
}

// Status is the cluster as the tools report it.
type Status struct {
	Context        string       `json:"context"`
	ChangesAllowed bool         `json:"changesAllowed"`
	Nodes          []NodeStatus `json:"nodes"`
	Pods           []PodStatus  `json:"pods"`
	DCGMPods       []PodStatus  `json:"dcgmPods"`
	Workload       []PodStatus  `json:"workload"`
}

func gpuChecks(node *corev1.Node) (reported, failing []string) {
	reported, failing = []string{}, []string{}

	for _, condition := range node.Status.Conditions {
		name := string(condition.Type)
		if !strings.HasPrefix(name, "Gpu") {
			continue
		}

		reported = append(reported, name)

		if condition.Status == corev1.ConditionTrue {
			failing = append(failing, name)
		}
	}

	sort.Strings(failing)

	return reported, failing
}

func nodeReady(node *corev1.Node) bool {
	return slices.ContainsFunc(node.Status.Conditions, func(condition corev1.NodeCondition) bool {
		return condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
	})
}

// Status reads the nodes and the pods the tests care about.
func (l *Lab) Status(ctx context.Context) (*Status, error) {
	nodes, err := l.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, apiFailure("listing nodes", err)
	}

	status := &Status{Context: l.context, ChangesAllowed: l.allowChanges, Nodes: []NodeStatus{}}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		reported, failing := gpuChecks(node)
		status.Nodes = append(status.Nodes, NodeStatus{
			Name:          node.Name,
			Ready:         nodeReady(node),
			Cordoned:      node.Spec.Unschedulable,
			State:         node.Labels[stateLabel],
			FailingChecks: failing,
			ChecksTotal:   len(reported),
		})
	}

	if status.Pods, err = l.podStatuses(ctx, nvsentinelNamespace, ""); err != nil {
		return nil, err
	}

	if status.DCGMPods, err = l.podStatuses(ctx, dcgmNamespace, dcgmSelector); err != nil {
		return nil, err
	}

	if status.Workload, err = l.podStatuses(ctx, workloadNamespace, workloadSelector); err != nil {
		return nil, err
	}

	return status, nil
}

func (l *Lab) podStatuses(ctx context.Context, namespace, selector string) ([]PodStatus, error) {
	pods, err := l.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, apiFailure("listing pods in "+namespace, err)
	}

	out := make([]PodStatus, 0, len(pods.Items))

	for i := range pods.Items {
		pod := &pods.Items[i]
		status := PodStatus{
			Name:  pod.Name,
			Node:  pod.Spec.NodeName,
			Phase: string(pod.Status.Phase),
			Ready: len(pod.Status.ContainerStatuses) > 0,
		}

		for _, container := range pod.Status.ContainerStatuses {
			status.Ready = status.Ready && container.Ready
			status.Restarts += container.RestartCount
		}

		out = append(out, status)
	}

	return out, nil
}

// LogComponents maps each component the logs tool accepts to its pods.
var LogComponents = map[string]string{
	"gpu-health-monitor":  "app.kubernetes.io/name=gpu-health-monitor",
	"platform-connectors": "app.kubernetes.io/name=nvsentinel",
	"fault-quarantine":    "app.kubernetes.io/name=fault-quarantine",
	"node-drainer":        "app.kubernetes.io/name=node-drainer",
}

// Logs returns the end of a component's logs, capped, cleaned and fenced as data.
func (l *Lab) Logs(ctx context.Context, component string, tail int64) (string, error) {
	selector, ok := LogComponents[component]
	if !ok {
		return "", failf("unknown component %q", component)
	}

	pods, err := l.client.CoreV1().Pods(nvsentinelNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", apiFailure("listing "+component+" pods", err)
	}

	lines := min(max(tail, 1), MaxLogLines)

	var output strings.Builder

	for i := range pods.Items {
		pod := &pods.Items[i]
		for c := range pod.Spec.Containers {
			if output.Len() >= MaxOutputBytes {
				break
			}

			text, err := l.containerLogs(ctx, pod.Name, pod.Spec.Containers[c].Name, lines)
			if err != nil {
				return "", err
			}

			fmt.Fprintf(&output, "[%s/%s]\n%s\n", pod.Name, pod.Spec.Containers[c].Name, text)
		}
	}

	return fenced(output.String())
}

func (l *Lab) containerLogs(ctx context.Context, pod, container string, lines int64) (string, error) {
	limit := int64(MaxOutputBytes)
	options := &corev1.PodLogOptions{Container: container, TailLines: &lines, LimitBytes: &limit}

	stream, err := l.client.CoreV1().Pods(nvsentinelNamespace).GetLogs(pod, options).Stream(ctx)
	if err != nil {
		return "", apiFailure("reading logs of "+pod, err)
	}
	defer stream.Close()

	text, err := io.ReadAll(io.LimitReader(stream, limit))
	if err != nil {
		return "", apiFailure("reading logs of "+pod, err)
	}

	return string(text), nil
}

func fenced(text string) (string, error) {
	out, err := Fence(text)
	if err != nil {
		return "", fmt.Errorf("fencing output: %w", err)
	}

	return out, nil
}

// NodeEvents returns the GPU node's recent events, capped, cleaned and fenced as data.
func (l *Lab) NodeEvents(ctx context.Context, limit int) (string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return "", err
	}

	selector := "involvedObject.kind=Node,involvedObject.name=" + node.Name

	events, err := l.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		return "", apiFailure("listing events", err)
	}

	items := events.Items
	sort.SliceStable(items, func(a, b int) bool { return items[a].LastTimestamp.Before(&items[b].LastTimestamp) })
	items = items[max(0, len(items)-min(max(limit, 1), MaxEvents)):]

	var lines strings.Builder

	for i := range items {
		row, err := json.Marshal(map[string]string{
			"time":    items[i].LastTimestamp.UTC().Format(time.RFC3339),
			"reason":  items[i].Reason,
			"message": Clean(items[i].Message, maxEventMessageSize),
		})
		if err != nil {
			return "", fmt.Errorf("encoding an event: %w", err)
		}

		lines.Write(row)
		lines.WriteByte('\n')
	}

	return fenced(lines.String())
}

// InjectResult says what was injected where.
type InjectResult struct {
	Node string `json:"node"`
	Pod  string `json:"pod"`
	XID  int    `json:"xid"`
}

// AllowedXIDs are the XIDs that were tested. The command is otherwise fixed.
var AllowedXIDs = []int{XID95}

// InjectFault writes an XID into the simulated hostengine.
func (l *Lab) InjectFault(ctx context.Context, xid int) (*InjectResult, error) {
	if !slices.Contains(AllowedXIDs, xid) {
		return nil, failf("XID %d is not allowed", xid)
	}

	if err := l.RequireLab(ctx); err != nil {
		return nil, err
	}

	node, err := l.gpuNode(ctx)
	if err != nil {
		return nil, err
	}

	pod, err := l.fakeDCGMPod(ctx, node.Name)
	if err != nil {
		return nil, err
	}

	command := []string{"dcgmi", "test", "--inject", "--gpuid", "0", "-f", xidField, "-v", strconv.Itoa(xid)}

	// The pod's output stays out of the result: it is cluster text, and this call does not fence.
	output, err := l.execer.Exec(ctx, dcgmNamespace, pod.Name, command)
	if err != nil {
		return nil, apiFailure("injecting into "+pod.Name, err)
	}

	slog.Debug("injected", "pod", pod.Name, "xid", xid, "output", Clean(output, maxErrorChars))

	return &InjectResult{Node: node.Name, Pod: pod.Name, XID: xid}, nil
}

// restartFakeDCGM deletes the simulated hostengine and waits for its replacement.
func (l *Lab) restartFakeDCGM(ctx context.Context) (string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return "", err
	}

	pod, err := l.fakeDCGMPod(ctx, node.Name)
	if err != nil {
		return "", err
	}

	err = l.client.CoreV1().Pods(dcgmNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		return "", apiFailure("deleting "+pod.Name, err)
	}

	result, err := l.WaitFor(ctx, StageDCGMPodReady, 3*time.Minute)
	if err != nil {
		return "", err
	}

	if !result.Reached {
		return "", failf("no DCGM pod came back: %s", result.Detail)
	}

	return pod.Name, nil
}

// ClearFault restarts the simulated hostengine. The fault lives in it, so this stands in for a reboot.
func (l *Lab) ClearFault(ctx context.Context) (map[string]string, error) {
	if err := l.RequireLab(ctx); err != nil {
		return nil, err
	}

	deleted, err := l.restartFakeDCGM(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]string{"deleted": deleted}, nil
}

func (l *Lab) pointDCGMService(ctx context.Context, app string) error {
	patch := fmt.Sprintf(`{"spec":{"selector":{"app":%q}}}`, app)

	_, err := l.client.CoreV1().Services(dcgmNamespace).Patch(
		ctx, dcgmService, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return apiFailure("patching the "+dcgmService+" Service", err)
	}

	return nil
}

// StartDCGMOutage points the DCGM Service at nothing and restarts the hostengine. The
// restart matters: an established connection outlives a Service change.
func (l *Lab) StartDCGMOutage(ctx context.Context) error {
	if err := l.RequireLab(ctx); err != nil {
		return err
	}

	node, err := l.gpuNode(ctx)
	if err != nil {
		return err
	}

	if _, err := l.fakeDCGMPod(ctx, node.Name); err != nil {
		return err
	}

	if err := l.pointDCGMService(ctx, dcgmOutageApp); err != nil {
		return err
	}

	_, err = l.restartFakeDCGM(ctx)

	return err
}

// EndDCGMOutage points the DCGM Service back at the hostengine.
func (l *Lab) EndDCGMOutage(ctx context.Context) error {
	if err := l.RequireLab(ctx); err != nil {
		return err
	}

	return l.pointDCGMService(ctx, dcgmApp)
}

// Reset undoes anything the tests can do: a Service outage and an injected fault.
func (l *Lab) Reset(ctx context.Context) error {
	if err := l.EndDCGMOutage(ctx); err != nil {
		return err
	}

	_, err := l.ClearFault(ctx)

	return err
}

// callTimeout bounds one API call in a path with no other deadline.
func callTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, apiTimeout)
}
