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

// Package main runs the MCP server for testing NVSentinel, over stdio.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/omerfsen/nvidia-nvsentinel-mcp/pkg/lab"
	"github.com/omerfsen/nvidia-nvsentinel-mcp/pkg/server"
)

// Populated during the build.
var version = "dev"

// Long enough for a running scenario to reset the lab.
const shutdownTimeout = 6 * time.Minute

func main() {
	if err := run(); err != nil {
		slog.Error("the server stopped", "error", err)
		os.Exit(1)
	}
}

// buildVersion falls back to the module version, which `go install ...@vX.Y.Z` records.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && info.Main.Version != "" {
		return info.Main.Version
	}

	return version
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// After the first signal the default action is back, so a second one ends the process.
	context.AfterFunc(ctx, stop)

	config := lab.ConfigFromEnv()
	mcpServer := server.New(ctx, buildVersion(), func() (*lab.Lab, error) { return lab.Open(config) }, logger)

	err := mcpServer.MCP().Run(ctx, &mcp.StdioTransport{})

	// The client left or a signal came: let a running scenario reset the lab first.
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if waitErr := mcpServer.Wait(waitCtx); waitErr != nil {
		return waitErr
	}

	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, mcp.ErrConnectionClosed) {
		return nil
	}

	return err
}
