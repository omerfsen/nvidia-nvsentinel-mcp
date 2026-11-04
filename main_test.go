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

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func parse(args ...string) (options, int, bool, string, string) {
	var stdout, stderr bytes.Buffer

	parsed, code, done := parseFlags(args, &stdout, &stderr)

	return parsed, code, done, stdout.String(), stderr.String()
}

func TestHelpGoesToStdoutAndEnds(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		_, code, done, stdout, stderr := parse(flag)
		assert.Equal(t, 0, code, flag)
		assert.True(t, done, flag)
		assert.Contains(t, stdout, "NVSENTINEL_MCP_ALLOW_CHANGES", flag)
		assert.Empty(t, stderr, flag)
	}
}

func TestVersionGoesToStdoutAndEnds(t *testing.T) {
	version = "1.2.3"

	defer func() { version = "dev" }()

	for _, flag := range []string{"-v", "--version"} {
		_, code, done, stdout, _ := parse(flag)
		assert.Equal(t, 0, code, flag)
		assert.True(t, done, flag)
		assert.Equal(t, "1.2.3\n", stdout, flag)
	}
}

func TestDebugRunsTheServer(t *testing.T) {
	for _, flag := range []string{"-d", "--debug"} {
		parsed, _, done, _, _ := parse(flag)
		assert.False(t, done, flag)
		assert.True(t, parsed.debug, flag)
	}
}

func TestNoFlagsRunTheServerWithoutDebug(t *testing.T) {
	parsed, _, done, stdout, _ := parse()
	assert.False(t, done)
	assert.False(t, parsed.debug)
	assert.Empty(t, stdout, "stdout belongs to the MCP protocol")
}

func TestABadCommandLineIsRefused(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"serve"}} {
		_, code, done, stdout, stderr := parse(args...)
		assert.Equal(t, 2, code, args)
		assert.True(t, done, args)
		assert.Empty(t, stdout, args)
		assert.Contains(t, stderr, "Usage:", args)
	}
}
