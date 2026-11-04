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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/omerfsen/nvidia-nvsentinel-mcp/pkg/lab"
)

const (
	labContext = "kind-nvsentinel-lab"
	node       = "lab-worker"
	fakeImage  = "ghcr.io/nvidia/nvsentinel-fake-dcgm:4.2.0"
)

// gate blocks an injection until the test lets it go.
type gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate { return &gate{entered: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) Exec(ctx context.Context, _, _ string, _ []string) (string, error) {
	g.once.Do(func() { close(g.entered) })

	select {
	case <-g.release:
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type passExec struct{}

func (passExec) Exec(context.Context, string, string, []string) (string, error) { return "ok", nil }

func healthyCluster() *fake.Clientset {
	return fake.NewClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: node, Labels: map[string]string{"nvidia.com/gpu.present": "true"}},
			Spec:       corev1.NodeSpec{ProviderID: "kind://docker/lab/" + node},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: "GpuDcgmConnectivityFailure", Status: corev1.ConditionFalse},
				{Type: "GpuMemWatch", Status: corev1.ConditionFalse},
			}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nvidia-dcgm-0", Namespace: "gpu-operator", Labels: map[string]string{"app": "nvidia-dcgm"},
			},
			Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Image: fakeImage}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gpu-workload-0", Namespace: "default", Labels: map[string]string{"app": "gpu-workload"},
			},
			Spec: corev1.PodSpec{NodeName: node},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-workload", Namespace: "default"},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "nvidia-dcgm", Namespace: "gpu-operator"}},
	)
}

type harness struct {
	server  *Server
	session *mcp.ClientSession
	client  *fake.Clientset
	audit   *bytes.Buffer
}

func start(t *testing.T, config lab.Config, contextName string, execer lab.Execer) *harness {
	t.Helper()

	client := healthyCluster()
	audit := &bytes.Buffer{}
	open := func() (*lab.Lab, error) {
		l := lab.New(client, execer, contextName, config)
		l.SetPoll(time.Millisecond)

		return l, nil
	}

	return connect(t, open, client, audit)
}

func connect(t *testing.T, open OpenLab, client *fake.Clientset, audit *bytes.Buffer) *harness {
	t.Helper()

	return connectWith(t, t.Context(), open, client, audit)
}

func connectWith(
	t *testing.T, shutdown context.Context, open OpenLab, client *fake.Clientset, audit *bytes.Buffer,
) *harness {
	t.Helper()

	server := New(shutdown, "test", open, slog.New(slog.NewJSONHandler(audit, nil)))
	serverSide, clientSide := mcp.NewInMemoryTransports()

	serverSession, err := server.MCP().Connect(t.Context(), serverSide, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), clientSide, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return &harness{server: server, session: session, client: client, audit: audit}
}

func labConfig() lab.Config { return lab.Config{LabContext: labContext, AllowChanges: true} }

func (h *harness) call(t *testing.T, tool string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()

	result, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		// Some SDK versions report bad arguments as a protocol error: still a refusal.
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
	}

	return result
}

func text(result *mcp.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}

	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}

	return content.Text
}

func (h *harness) changed() bool {
	for _, action := range h.client.Actions() {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete":
			return true
		}
	}

	return false
}

func TestToolsAreListedAndMarkedReadOrChange(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})

	tools, err := h.session.ListTools(t.Context(), nil)
	require.NoError(t, err)

	want := map[string]bool{
		"lab_status": true, "list_scenarios": true, "wait_for_stage": true, "component_logs": true, "node_events": true,
		"inject_fault": false, "clear_fault": false, "reset_lab": false, "run_test_scenario": false,
	}

	got := map[string]bool{}

	for _, tool := range tools.Tools {
		got[tool.Name] = tool.Annotations.ReadOnlyHint
		require.NotNil(t, tool.Annotations.DestructiveHint, tool.Name)
		assert.Equal(t, !tool.Annotations.ReadOnlyHint, *tool.Annotations.DestructiveHint, tool.Name)
		assert.NotEmpty(t, tool.Description, tool.Name)
	}

	assert.Equal(t, want, got)
}

func TestSchemasNameTheAllowedValues(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})

	tools, err := h.session.ListTools(t.Context(), nil)
	require.NoError(t, err)

	schemas := map[string]string{}

	for _, tool := range tools.Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)

		schemas[tool.Name] = string(encoded)
	}

	assert.Contains(t, schemas["run_test_scenario"], "xid95")
	assert.Contains(t, schemas["wait_for_stage"], `"maximum":900`)
	assert.Contains(t, schemas["component_logs"], `"maximum":2000`)
	assert.Contains(t, schemas["component_logs"], "node-drainer")
	assert.Contains(t, schemas["inject_fault"], `"enum":[95]`)
}

func TestBadArgumentsAreRefusedAndNeverReachTheCluster(t *testing.T) {
	cases := map[string]struct {
		tool      string
		arguments map[string]any
	}{
		"unknown scenario":     {"run_test_scenario", map[string]any{"name": "nope"}},
		"unlisted xid":         {"inject_fault", map[string]any{"xid": 13}},
		"xid as a string":      {"inject_fault", map[string]any{"xid": "95; rm -rf /"}},
		"huge timeout":         {"wait_for_stage", map[string]any{"stage": "cordoned", "timeoutSeconds": 1_000_000_000}},
		"flag as a stage":      {"wait_for_stage", map[string]any{"stage": "--help"}},
		"flag as a source":     {"component_logs", map[string]any{"component": "--help"}},
		"huge tail":            {"component_logs", map[string]any{"component": "node-drainer", "tail": 1_000_000_000}},
		"huge limit":           {"node_events", map[string]any{"limit": 1_000_000_000}},
		"an extra field":       {"inject_fault", map[string]any{"xid": 95, "kubeconfig": "/etc/passwd"}},
		"an extra on none":     {"lab_status", map[string]any{"kubeconfig": "/etc/passwd"}},
		"an extra on a change": {"reset_lab", map[string]any{"kubeconfig": "/etc/passwd"}},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := start(t, labConfig(), labContext, passExec{})
			result := h.call(t, test.tool, test.arguments)
			assert.True(t, result.IsError, "%+v", result)
			assert.Contains(t, text(result), "validating", "refused by the schema, before any code ran")
			assert.Empty(t, h.client.Actions())
		})
	}
}

func TestARefusalComesBackAsAToolError(t *testing.T) {
	h := start(t, labConfig(), "rke2-production", passExec{})
	result := h.call(t, "inject_fault", map[string]any{"xid": 95})
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "not the lab's")
	assert.False(t, h.changed())
}

func TestChangesOffComesBackAsAToolError(t *testing.T) {
	h := start(t, lab.Config{LabContext: labContext}, labContext, passExec{})
	result := h.call(t, "run_test_scenario", map[string]any{"name": "xid95"})
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "changes are off")
	assert.False(t, h.changed())
}

func TestACrashShowsNoDetail(t *testing.T) {
	secret := "token=abc123"
	open := func() (*lab.Lab, error) { return nil, errors.New(secret) }
	h := connect(t, open, fake.NewClientset(), &bytes.Buffer{})

	result := h.call(t, "lab_status", nil)
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "error executing tool lab_status")
	assert.NotContains(t, text(result), secret)
}

func TestAPanicShowsNoDetail(t *testing.T) {
	open := func() (*lab.Lab, error) { panic("token=abc123") }
	h := connect(t, open, fake.NewClientset(), &bytes.Buffer{})

	for _, tool := range []string{"lab_status", "clear_fault"} {
		result := h.call(t, tool, nil)
		assert.True(t, result.IsError, tool)
		assert.NotContains(t, text(result), "abc123", tool)
	}

	// The lock was released, so the next change is not told another one is running.
	result := h.call(t, "clear_fault", nil)
	assert.NotContains(t, text(result), "another change")
}

func TestOnlyOneChangeRunsAtATime(t *testing.T) {
	blocker := newGate()
	h := start(t, labConfig(), labContext, blocker)

	done := make(chan *mcp.CallToolResult, 1)

	go func() {
		result, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "inject_fault", Arguments: map[string]any{"xid": 95},
		})
		if err == nil {
			done <- result
		}
	}()

	<-blocker.entered

	second := h.call(t, "clear_fault", nil)
	assert.True(t, second.IsError)
	assert.Contains(t, text(second), "another change is running")

	close(blocker.release)
	assert.False(t, (<-done).IsError)
	assert.Contains(t, h.audit.String(), `"outcome":"busy"`)
}

func TestEveryChangeIsAudited(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})
	h.call(t, "inject_fault", map[string]any{"xid": 95})

	var record map[string]any

	require.NoError(t, json.Unmarshal(bytes.TrimSpace(h.audit.Bytes()), &record))
	assert.Equal(t, "inject_fault", record["tool"])
	assert.Equal(t, "ok", record["outcome"])
	assert.Contains(t, record["arguments"], "95")
}

func TestARefusedChangeIsAudited(t *testing.T) {
	h := start(t, labConfig(), "rke2-production", passExec{})
	h.call(t, "clear_fault", nil)
	assert.Contains(t, h.audit.String(), `"outcome":"refused"`)
}

func TestReadingIsNotAudited(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})
	h.call(t, "lab_status", nil)
	assert.Empty(t, h.audit.String())
}

// The SDK does not cancel handlers on shutdown over stdio, so the server must.
func TestShutdownCancelsARunningChange(t *testing.T) {
	blocker := newGate()
	stop, cancel := context.WithCancel(t.Context())
	audit := &bytes.Buffer{}
	client := healthyCluster()
	open := func() (*lab.Lab, error) { return lab.New(client, blocker, labContext, labConfig()), nil }
	h := connectWith(t, stop, open, client, audit)

	go func() {
		_, _ = h.session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "inject_fault", Arguments: map[string]any{"xid": 95},
		})
	}()

	<-blocker.entered
	cancel()

	// The gate never opens: only the cancelled context can end this change.
	require.NoError(t, h.server.Wait(t.Context()))
	assert.Contains(t, h.audit.String(), `"outcome":"refused"`)
}

func TestShutdownWaitsForARunningChange(t *testing.T) {
	blocker := newGate()
	h := start(t, labConfig(), labContext, blocker)

	go func() {
		_, _ = h.session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "inject_fault", Arguments: map[string]any{"xid": 95},
		})
	}()

	<-blocker.entered

	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, h.server.Wait(short), context.DeadlineExceeded)

	close(blocker.release)
	require.NoError(t, h.server.Wait(t.Context()))
}

func TestWaitsAreLimited(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})

	for range maxConcurrentWaits {
		h.server.waits <- struct{}{}
	}

	result := h.call(t, "wait_for_stage", map[string]any{"stage": "healthy", "timeoutSeconds": 1})
	assert.True(t, result.IsError)
	assert.Contains(t, text(result), "waits are already running")
}

func TestStatusGoesThroughTheServer(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})
	result := h.call(t, "lab_status", nil)
	require.False(t, result.IsError, text(result))

	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), node)
	assert.Contains(t, string(encoded), `"changesAllowed":true`)
}

func TestTheCatalogListsWhatCanBeTested(t *testing.T) {
	h := start(t, labConfig(), labContext, passExec{})
	result := h.call(t, "list_scenarios", nil)
	require.False(t, result.IsError, text(result))

	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "dcgm_outage")
	assert.Contains(t, string(encoded), "drain_succeeded")
}
