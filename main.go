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
	"flag"
	"fmt"
	"io"
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

const (
	// Long enough for a running scenario to reset the lab.
	shutdownTimeout = 6 * time.Minute

	usage = `nvidia-nvsentinel-mcp: an MCP server for testing NVIDIA NVSentinel on a lab cluster.

It speaks MCP over stdin and stdout, so an MCP client starts it. Run by hand, it logs one
line to stderr and then waits for a client: press Ctrl-C to stop it.

Usage:
  nvidia-nvsentinel-mcp [flags]

Flags:
  -h, --help     show this help
  -v, --version  show the version
  -d, --debug    log each tool call and cluster detail to stderr

Environment:
  NVSENTINEL_MCP_KUBECONFIG     the one kubeconfig it uses, private (default
                                ~/.kube/nvsentinel-lab-mcp.yaml)
  NVSENTINEL_MCP_ALLOW_CHANGES  exactly "true" allows the change tools, on a kind cluster only
  NVSENTINEL_MCP_LAB_CONTEXT    the context changes need, starting with kind- (default
                                kind-nvsentinel-lab)

Docs: https://github.com/omerfsen/nvidia-nvsentinel-mcp
`
)

func main() {
	options, exitCode, done := parseFlags(os.Args[1:], os.Stdout, os.Stderr)
	if done {
		os.Exit(exitCode)
	}

	if err := run(options); err != nil {
		slog.Error("the server stopped", "error", err)
		os.Exit(1)
	}
}

type options struct {
	debug bool
}

// parseFlags reads the command line. When done is true, the program ends with exitCode.
func parseFlags(args []string, stdout, stderr io.Writer) (parsed options, exitCode int, done bool) {
	flags := flag.NewFlagSet("nvidia-nvsentinel-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var showHelp, showVersion bool

	flags.BoolVar(&showHelp, "h", false, "")
	flags.BoolVar(&showHelp, "help", false, "")
	flags.BoolVar(&showVersion, "v", false, "")
	flags.BoolVar(&showVersion, "version", false, "")
	flags.BoolVar(&parsed.debug, "d", false, "")
	flags.BoolVar(&parsed.debug, "debug", false, "")

	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "%v\n\n%s", err, usage)
		return parsed, 2, true
	}

	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n\n%s", flags.Arg(0), usage)
		return parsed, 2, true
	}

	switch {
	case showHelp:
		fmt.Fprint(stdout, usage)
		return parsed, 0, true
	case showVersion:
		fmt.Fprintln(stdout, buildVersion())
		return parsed, 0, true
	}

	return parsed, 0, false
}

// buildVersion falls back to the module version, which `go install ...@vX.Y.Z` records.
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && info.Main.Version != "" {
		return info.Main.Version
	}

	return version
}

func run(opts options) error {
	level := slog.LevelInfo
	if opts.debug {
		level = slog.LevelDebug
	}

	// Stderr only: stdout belongs to the MCP protocol.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// After the first signal the default action is back, so a second one ends the process.
	context.AfterFunc(ctx, stop)

	config := lab.ConfigFromEnv()
	mcpServer := server.New(ctx, buildVersion(), func() (*lab.Lab, error) { return lab.Open(config) }, logger)

	logger.Info("started: waiting for an MCP client on stdin (see --help)",
		"version", buildVersion(), "changesAllowed", config.AllowChanges)

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
