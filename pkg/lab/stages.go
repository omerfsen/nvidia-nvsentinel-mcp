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

package lab

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The stages a test can wait for. Each one reads an effect from the cluster.
const (
	StageDCGMConnected   = "dcgm_connected"
	StageDCGMUnreachable = "dcgm_unreachable"
	StageDCGMPodReady    = "dcgm_pod_ready"
	StageFaultDetected   = "fault_detected"
	StageHealthy         = "healthy"
	StageCordoned        = "cordoned"
	StageUncordoned      = "uncordoned"
	StageWorkloadEvicted = "workload_evicted"
	StageDrainSucceeded  = "drain_succeeded"
	StateCleared         = "state_cleared"
	StageWorkloadRunning = "workload_running"
)

// Past these, a node is drained: the labels go quarantined, draining, then these.
var drainedStates = []string{"drain-succeeded", "remediating", "remediation-succeeded"}

type stageCheck func(context.Context, *Lab) (bool, string, error)

func stageChecks() map[string]stageCheck {
	return map[string]stageCheck{
		StageDCGMConnected:   connectivityIs(corev1.ConditionFalse),
		StageDCGMUnreachable: connectivityIs(corev1.ConditionTrue),
		StageDCGMPodReady:    dcgmPodReady,
		StageFaultDetected:   faultDetected,
		StageHealthy:         healthy,
		StageCordoned:        cordonIs(true),
		StageUncordoned:      cordonIs(false),
		StageWorkloadEvicted: workloadEvicted,
		StageDrainSucceeded:  drainSucceeded,
		StateCleared:         stateCleared,
		StageWorkloadRunning: workloadRunning,
	}
}

// Stages lists the stage names.
func Stages() []string {
	names := make([]string, 0, len(stageChecks()))
	for name := range stageChecks() {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func connectivityIs(want corev1.ConditionStatus) stageCheck {
	return func(ctx context.Context, l *Lab) (bool, string, error) {
		node, err := l.gpuNode(ctx)
		if err != nil {
			return false, "", err
		}

		var value corev1.ConditionStatus

		for _, condition := range node.Status.Conditions {
			if string(condition.Type) == connectivityCheck {
				value = condition.Status
			}
		}

		return value == want, fmt.Sprintf("%s=%s", connectivityCheck, value), nil
	}
}

func podReady(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
		return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
	})
}

func dcgmPodReady(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	pods, err := l.dcgmPods(ctx, node.Name)
	if err != nil {
		return false, "", err
	}

	ready := 0

	for i := range pods {
		if podReady(&pods[i]) {
			ready++
		}
	}

	return ready > 0, fmt.Sprintf("%d of %d DCGM pods ready", ready, len(pods)), nil
}

func faultDetected(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	_, failing := gpuChecks(node)
	failing = slices.DeleteFunc(failing, func(name string) bool { return name == connectivityCheck })

	return len(failing) > 0, fmt.Sprintf("failing: %v", failing), nil
}

func healthy(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	reported, failing := gpuChecks(node)

	return len(reported) > 0 && len(failing) == 0,
		fmt.Sprintf("%d checks reported, failing: %v", len(reported), failing), nil
}

func cordonIs(want bool) stageCheck {
	return func(ctx context.Context, l *Lab) (bool, string, error) {
		node, err := l.gpuNode(ctx)
		if err != nil {
			return false, "", err
		}

		return node.Spec.Unschedulable == want, fmt.Sprintf("unschedulable=%t", node.Spec.Unschedulable), nil
	}
}

func (l *Lab) workloadNodes(ctx context.Context) ([]string, error) {
	pods, err := l.client.CoreV1().Pods(workloadNamespace).List(ctx, metav1.ListOptions{LabelSelector: workloadSelector})
	if err != nil {
		return nil, failf("listing workload pods: %v", err)
	}

	nodes := []string{}

	for i := range pods.Items {
		if pods.Items[i].Spec.NodeName != "" && pods.Items[i].DeletionTimestamp == nil {
			nodes = append(nodes, pods.Items[i].Spec.NodeName)
		}
	}

	return nodes, nil
}

func workloadEvicted(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	nodes, err := l.workloadNodes(ctx)
	if err != nil {
		return false, "", err
	}

	return !slices.Contains(nodes, node.Name), fmt.Sprintf("workload pods on %v", nodes), nil
}

func drainSucceeded(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	state := node.Labels[stateLabel]

	return slices.Contains(drainedStates, state), "state=" + state, nil
}

func stateCleared(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	state, set := node.Labels[stateLabel]

	return !set, "state=" + state, nil
}

func workloadRunning(ctx context.Context, l *Lab) (bool, string, error) {
	node, err := l.gpuNode(ctx)
	if err != nil {
		return false, "", err
	}

	deployment, err := l.client.AppsV1().Deployments(workloadNamespace).Get(ctx, workloadName, metav1.GetOptions{})
	if err != nil {
		return false, "", failf("reading the workload: %v", err)
	}

	nodes, err := l.workloadNodes(ctx)
	if err != nil {
		return false, "", err
	}

	onNode := slices.Contains(nodes, node.Name)

	return deployment.Status.ReadyReplicas >= 1 && onNode,
		fmt.Sprintf("readyReplicas=%d, on the GPU node: %t", deployment.Status.ReadyReplicas, onNode), nil
}

// WaitResult is how a wait ended.
type WaitResult struct {
	Stage   string  `json:"stage"`
	Reached bool    `json:"reached"`
	Seconds float64 `json:"seconds"`
	Detail  string  `json:"detail"`
}

// WaitFor polls a stage until it holds, the timeout passes or ctx ends. A failed query is
// not progress: it is reported, never read as reached.
func (l *Lab) WaitFor(ctx context.Context, stage string, timeout time.Duration) (*WaitResult, error) {
	check, ok := stageChecks()[stage]
	if !ok {
		return nil, failf("unknown stage %q", stage)
	}

	limit := min(timeout, MaxWait)
	start := time.Now()
	result := &WaitResult{Stage: stage}

	for {
		callCtx, cancel := callTimeout(ctx)
		reached, detail, err := check(callCtx, l)

		cancel()

		result.Reached, result.Detail = reached, detail
		if err != nil {
			result.Reached, result.Detail = false, "query failed: "+err.Error()
		}

		result.Seconds = time.Since(start).Round(100 * time.Millisecond).Seconds()
		if result.Reached || time.Since(start) >= limit {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for %s: %w", stage, ctx.Err())
		case <-time.After(l.poll):
		}
	}
}

// waitOrFail is WaitFor for a step that must reach its stage.
func (l *Lab) waitOrFail(ctx context.Context, stage string, timeout time.Duration) (*WaitResult, error) {
	result, err := l.WaitFor(ctx, stage, timeout)
	if err != nil {
		return nil, err
	}

	if !result.Reached {
		return result, failf("%s not reached in %s: %s", stage, timeout, result.Detail)
	}

	return result, nil
}
