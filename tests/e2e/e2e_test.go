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

//go:build e2e

// Package e2e runs the scenarios on the kind lab through the real server binary, over
// MCP on stdio, as a client would. `make e2e` runs it; E2E_RESULTS writes the reports.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scenarioTimeout = 15 * time.Minute

func kubeconfig(t *testing.T) string {
	t.Helper()

	if path := os.Getenv("NVSENTINEL_MCP_KUBECONFIG"); path != "" {
		return path
	}

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	return filepath.Join(home, ".kube", "nvsentinel-lab-mcp.yaml")
}

func serverCommand(t *testing.T, path string, allowChanges bool) *exec.Cmd {
	t.Helper()

	allow := "false"
	if allowChanges {
		allow = "true"
	}

	if image := os.Getenv("E2E_IMAGE"); image != "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		if path == kubeconfig(t) {
			path = filepath.Join(home, ".kube", "nvsentinel-lab-mcp-docker.yaml")
		}

		//nolint:gosec // The arguments come from the test's own environment.
		return exec.Command("docker", "run", "--rm", "-i", "--read-only", "--cap-drop=ALL",
			"--security-opt=no-new-privileges", "--user", strings.TrimSpace(ids(t)), "--memory=128m", "--cpus=1",
			"--pids-limit=64", "--network=kind", "--volume", path+":/kube/config:ro",
			"--env", "NVSENTINEL_MCP_ALLOW_CHANGES="+allow, image)
	}

	binary := os.Getenv("E2E_BINARY")
	require.NotEmpty(t, binary, "E2E_BINARY names the server binary")

	command := exec.Command(binary) //nolint:gosec // The test's own build.
	command.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"NVSENTINEL_MCP_KUBECONFIG=" + path,
		"NVSENTINEL_MCP_ALLOW_CHANGES=" + allow,
	}
	command.Stderr = os.Stderr

	return command
}

func ids(t *testing.T) string {
	t.Helper()

	return strings.Join([]string{itoa(os.Getuid()), itoa(os.Getgid())}, ":")
}

func itoa(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func connect(t *testing.T, command *exec.Cmd) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "test"}, nil)

	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func call(t *testing.T, session *mcp.ClientSession, tool string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), scenarioTimeout)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	require.NoError(t, err, tool)

	return result
}

func message(result *mcp.CallToolResult) string {
	if len(result.Content) > 0 {
		if text, ok := result.Content[0].(*mcp.TextContent); ok {
			return text.Text
		}
	}

	return ""
}

func save(t *testing.T, name string, value any) {
	t.Helper()

	directory := os.Getenv("E2E_RESULTS")
	if directory == "" {
		return
	}

	encoded, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, name+".json"), append(encoded, '\n'), 0o600))
}

type report struct {
	Scenario string `json:"scenario"`
	Passed   bool   `json:"passed"`
	Failed   string `json:"failedStep"`
	Restored string `json:"restored"`
}

func runScenario(t *testing.T, name string) {
	t.Helper()

	session := connect(t, serverCommand(t, kubeconfig(t), true))
	result := call(t, session, "run_test_scenario", map[string]any{"name": name})
	require.False(t, result.IsError, message(result))

	save(t, name, result.StructuredContent)

	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var outcome report

	require.NoError(t, json.Unmarshal(encoded, &outcome))
	assert.True(t, outcome.Passed, "failed at %q: %s", outcome.Failed, encoded)
}

// The image must not run as root, whatever flags it is started with.
func TestImageDoesNotRunAsRoot(t *testing.T) {
	image := os.Getenv("E2E_IMAGE")
	if image == "" {
		t.Skip("no image under test")
	}

	output, err := exec.Command("docker", "inspect", "--format", "{{.Config.User}}", image).Output() //nolint:gosec // The test's own image.
	require.NoError(t, err)
	assert.Equal(t, "65532:65532", strings.TrimSpace(string(output)))
}

func TestXID95(t *testing.T) { runScenario(t, "xid95") }

func TestDCGMOutage(t *testing.T) { runScenario(t, "dcgm_outage") }

func TestStatusAfterTheScenarios(t *testing.T) {
	session := connect(t, serverCommand(t, kubeconfig(t), false))
	result := call(t, session, "lab_status", nil)
	require.False(t, result.IsError, message(result))

	save(t, "status", result.StructuredContent)

	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"changesAllowed":false`)
}

func TestLogsAndEventsAreFenced(t *testing.T) {
	session := connect(t, serverCommand(t, kubeconfig(t), false))

	logs := call(t, session, "component_logs", map[string]any{"component": "fault-quarantine", "tail": 5})
	require.False(t, logs.IsError, message(logs))
	assert.Contains(t, message(logs), "untrusted-cluster-data-")

	events := call(t, session, "node_events", map[string]any{"limit": 5})
	require.False(t, events.IsError, message(events))
	assert.Contains(t, message(events), "untrusted-cluster-data-")
}

func TestChangesAreOffByDefault(t *testing.T) {
	session := connect(t, serverCommand(t, kubeconfig(t), false))
	result := call(t, session, "inject_fault", map[string]any{"xid": 95})
	require.True(t, result.IsError)
	assert.Contains(t, message(result), "changes are off")
	save(t, "refused-changes-off", map[string]any{"tool": "inject_fault", "isError": true, "message": message(result)})
}

// The same token under another context name must be refused, with changes allowed.
func TestAnotherContextIsRefused(t *testing.T) {
	source, err := os.ReadFile(kubeconfig(t))
	require.NoError(t, err)

	renamed := strings.ReplaceAll(string(source), "kind-nvsentinel-lab", "rke2-production")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(renamed), 0o600))

	session := connect(t, serverCommand(t, path, true))
	result := call(t, session, "inject_fault", map[string]any{"xid": 95})
	require.True(t, result.IsError)
	assert.Contains(t, message(result), "not the lab's")
	save(t, "refused-other-context", map[string]any{"tool": "inject_fault", "isError": true, "message": message(result)})
}

// SIGTERM mid-scenario: the server resets the lab before it exits.
func TestSIGTERMResetsTheLab(t *testing.T) {
	if os.Getenv("E2E_IMAGE") != "" {
		t.Skip("signals go to the docker client, not the server")
	}

	var stderr safeBuffer

	command := serverCommand(t, kubeconfig(t), true)
	command.Stderr = &stderr
	session := connect(t, command)

	done := make(chan struct{})

	go func() {
		defer close(done)

		ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
		defer cancel()

		_, _ = session.CallTool(ctx, &mcp.CallToolParams{
			Name: "run_test_scenario", Arguments: map[string]any{"name": "xid95"},
		})
	}()

	watcher := connect(t, serverCommand(t, kubeconfig(t), false))
	waitFor(t, watcher, "fault_detected")

	require.NoError(t, command.Process.Signal(syscall.SIGTERM))
	<-done
	require.NoError(t, waitForExit(command))

	// The scenario clears its own fault at its end, so a healthy lab alone proves nothing.
	// Its audit line says it stopped there and reset the lab instead.
	assert.Contains(t, stderr.String(), `"tool":"run_test_scenario"`)
	assert.Contains(t, stderr.String(), `"outcome":"cancelled"`)
	assert.Contains(t, stderr.String(), `"restored":"ok"`)

	waitFor(t, watcher, "healthy")
	waitFor(t, watcher, "uncordoned")
	waitFor(t, watcher, "state_cleared")
	waitFor(t, watcher, "workload_running")
}

// waitForExit reaps the server, which CommandTransport also waits for at Close.
func waitForExit(command *exec.Cmd) error {
	for range 600 {
		if command.ProcessState != nil || command.Process.Signal(syscall.Signal(0)) != nil {
			return nil
		}

		time.Sleep(time.Second)
	}

	return errors.New("the server did not exit")
}

type safeBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *safeBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.buffer.Write(data)
}

func (b *safeBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.buffer.String()
}

func waitFor(t *testing.T, session *mcp.ClientSession, stage string) {
	t.Helper()

	result := call(t, session, "wait_for_stage", map[string]any{"stage": stage, "timeoutSeconds": 300})
	require.False(t, result.IsError, message(result))

	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"reached":true`, stage)
}
