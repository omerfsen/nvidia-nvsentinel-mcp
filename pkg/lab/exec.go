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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
)

// Execer runs a fixed command in a pod.
type Execer interface {
	Exec(ctx context.Context, namespace, pod string, command []string) (string, error)
}

type podExecer struct {
	client kubernetes.Interface
	config *rest.Config
}

// Exec uses the websocket protocol and falls back to SPDY, as kubectl does.
func (e *podExecer) Exec(ctx context.Context, namespace, pod string, command []string) (string, error) {
	request := e.client.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)

	spdy, err := remotecommand.NewSPDYExecutor(e.config, http.MethodPost, request.URL())
	if err != nil {
		return "", fmt.Errorf("creating the SPDY executor: %w", err)
	}

	websocket, err := remotecommand.NewWebSocketExecutor(e.config, http.MethodGet, request.URL().String())
	if err != nil {
		return "", fmt.Errorf("creating the websocket executor: %w", err)
	}

	// Only when the websocket upgrade itself failed, as kubectl does. Any other error may
	// come from the command, and falling back would run it twice.
	executor, err := remotecommand.NewFallbackExecutor(websocket, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return "", fmt.Errorf("creating the executor: %w", err)
	}

	var stdout, stderr bytes.Buffer

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &limitedWriter{writer: &stdout, left: MaxOutputBytes},
		Stderr: &limitedWriter{writer: &stderr, left: MaxOutputBytes},
	})
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// limitedWriter keeps the first bytes and drops the rest, so a noisy pod cannot use up
// the server's memory in the middle of a change.
type limitedWriter struct {
	writer io.Writer
	left   int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if kept := min(len(data), w.left); kept > 0 {
		if _, err := w.writer.Write(data[:kept]); err != nil {
			return 0, fmt.Errorf("keeping output: %w", err)
		}

		w.left -= kept
	}

	return len(data), nil
}
