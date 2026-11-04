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
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

const (
	testNode  = "lab-worker"
	testImage = fakeDCGMRepository + ":4.2.0"
)

// world plays NVSentinel's part crudely, so the sequencing can run with no cluster: an
// injected fault cordons and drains at once, and a restarted hostengine undoes it. It
// tests this package and says nothing about NVSentinel.
type world struct {
	t          *testing.T
	client     *fake.Clientset
	mutex      sync.Mutex
	generation int
	execCalls  [][]string
	onExec     func()
}

func newWorld(t *testing.T) *world {
	t.Helper()

	w := &world{t: t}
	w.client = fake.NewClientset(
		w.node(false, false),
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "lab-control-plane"},
			Spec:       corev1.NodeSpec{ProviderID: "kind://docker/lab/lab-control-plane"},
		},
		dcgmPod("nvidia-dcgm-0", testImage),
		workloadPod(),
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: workloadNamespace},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: dcgmService, Namespace: dcgmNamespace},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": dcgmApp}},
		},
	)
	w.client.PrependReactor("delete", "pods", w.onDeletePod)
	w.client.PrependReactor("patch", "services", w.onPatchService)

	return w
}

func (w *world) node(fault, outage bool) *corev1.Node {
	status := func(on bool) corev1.ConditionStatus {
		if on {
			return corev1.ConditionTrue
		}

		return corev1.ConditionFalse
	}
	labels := map[string]string{"nvidia.com/gpu.present": "true"}

	if fault {
		labels[stateLabel] = "drain-succeeded"
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: testNode, Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: "kind://docker/lab/" + testNode, Unschedulable: fault},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			{Type: connectivityCheck, Status: status(outage)},
			{Type: "GpuInforomWatch", Status: corev1.ConditionFalse},
			{Type: "GpuMemWatch", Status: status(fault)},
		}},
	}
}

func dcgmPod(name, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dcgmNamespace, Labels: map[string]string{"app": dcgmApp}},
		Spec: corev1.PodSpec{
			NodeName:   testNode,
			Containers: []corev1.Container{{Name: "nvidia-dcgm-ctr", Image: image}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func workloadPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-workload-0", Namespace: workloadNamespace, Labels: map[string]string{"app": "gpu-workload"},
		},
		Spec: corev1.PodSpec{NodeName: testNode},
	}
}

func resource(name string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Version: "v1", Resource: name}
}

func (w *world) setNode(fault, outage bool) {
	w.t.Helper()

	if err := w.client.Tracker().Update(resource("nodes"), w.node(fault, outage), ""); err != nil {
		w.t.Fatalf("updating the node: %v", err)
	}
}

func (w *world) outage() bool {
	object, err := w.client.Tracker().Get(resource("services"), dcgmNamespace, dcgmService)
	if err != nil {
		w.t.Fatalf("reading the Service: %v", err)
	}

	return object.(*corev1.Service).Spec.Selector["app"] == dcgmOutageApp
}

func (w *world) setWorkload(running bool) {
	w.t.Helper()

	deployments := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: workloadNamespace},
	}

	if running {
		deployment.Status.ReadyReplicas = 1
		_ = w.client.Tracker().Add(workloadPod())
	} else {
		_ = w.client.Tracker().Delete(resource("pods"), workloadNamespace, "gpu-workload-0")
	}

	if err := w.client.Tracker().Update(deployments, deployment, workloadNamespace); err != nil {
		w.t.Fatalf("updating the workload: %v", err)
	}
}

// A deleted hostengine comes back with no injected state, as the DaemonSet would bring it.
func (w *world) onDeletePod(action clienttesting.Action) (bool, runtime.Object, error) {
	deletion := action.(clienttesting.DeleteAction)
	if deletion.GetNamespace() != dcgmNamespace {
		return false, nil, nil
	}

	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.generation++

	if err := w.client.Tracker().Delete(resource("pods"), dcgmNamespace, deletion.GetName()); err != nil {
		return true, nil, fmt.Errorf("deleting: %w", err)
	}

	if err := w.client.Tracker().Add(dcgmPod(fmt.Sprintf("nvidia-dcgm-%d", w.generation), testImage)); err != nil {
		return true, nil, fmt.Errorf("adding: %w", err)
	}

	w.setNode(false, w.outage())
	w.setWorkload(true)

	return true, nil, nil
}

func (w *world) onPatchService(action clienttesting.Action) (bool, runtime.Object, error) {
	patch := action.(clienttesting.PatchAction)
	outage := strings.Contains(string(patch.GetPatch()), dcgmOutageApp)

	selector := dcgmApp
	if outage {
		selector = dcgmOutageApp
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: dcgmService, Namespace: dcgmNamespace},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": selector}},
	}
	if err := w.client.Tracker().Update(resource("services"), service, dcgmNamespace); err != nil {
		return true, nil, fmt.Errorf("updating: %w", err)
	}

	w.setNode(false, outage)

	return true, service, nil
}

// Exec is the injection: the fault appears, and NVSentinel's work is done at once.
func (w *world) Exec(_ context.Context, _, _ string, command []string) (string, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.execCalls = append(w.execCalls, command)
	if w.onExec != nil {
		w.onExec()
	}

	w.setNode(true, false)
	w.setWorkload(false)

	return "Successfully injected\n", nil
}

func (w *world) changes() int {
	count := 0

	for _, action := range w.client.Actions() {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete":
			count++
		}
	}

	return count + len(w.execCalls)
}

func (w *world) lab(mutate ...func(*Config)) *Lab {
	config := Config{LabContext: defaultLabContext, AllowChanges: true}
	for _, change := range mutate {
		change(&config)
	}

	l := New(w.client, w, config.LabContext, config)
	l.SetPoll(time.Millisecond)

	return l
}
