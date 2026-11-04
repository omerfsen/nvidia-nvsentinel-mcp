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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

// Limits on what goes back to the caller.
const (
	MaxOutputBytes      = 64 * 1024
	maxErrorChars       = 500
	maxEventMessageSize = 512
)

// UserError is an expected failure or a refusal. Its message is safe to return to the
// caller; anything else that goes wrong is reported without detail.
type UserError struct {
	message string
}

func (e *UserError) Error() string { return e.message }

func failf(format string, args ...any) error {
	return &UserError{message: Clean(fmt.Sprintf(format, args...), maxErrorChars)}
}

// Clean removes control characters and terminal escapes, keeping tab and newline, and
// keeps the end of text longer than limit bytes.
func Clean(text string, limit int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}

		return -1
	}, text)

	if len(cleaned) <= limit {
		return cleaned
	}

	return "[truncated]\n" + strings.ToValidUTF8(cleaned[len(cleaned)-limit:], "")
}

// Fence marks cluster output as data. The marker is random on every call, so the text
// inside cannot close it.
func Fence(text string) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}

	marker := "untrusted-cluster-data-" + hex.EncodeToString(random)

	return fmt.Sprintf("<%s>\n%s\n</%s>", marker, Clean(text, MaxOutputBytes), marker), nil
}
