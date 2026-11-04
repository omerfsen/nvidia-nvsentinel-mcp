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
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

const (
	// ScenarioXID95 breaks the simulated GPU and checks the pipeline end to end.
	ScenarioXID95 = "xid95"
	// ScenarioDCGMOutage takes the simulated hostengine away and brings it back.
	ScenarioDCGMOutage = "dcgm_outage"

	restoreTimeout = 5 * time.Minute
	longWait       = 5 * time.Minute
	normalWait     = 3 * time.Minute
	shortWait      = time.Minute

	stoppedMessage = "stopped: the client left or the server is stopping"
)

type stepResult struct {
	Step    string  `json:"step"`
	OK      bool    `json:"ok"`
	Seconds float64 `json:"seconds"`
	Detail  string  `json:"detail,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// Report is a scenario's outcome, step by step.
type Report struct {
	Scenario   string       `json:"scenario"`
	Passed     bool         `json:"passed"`
	FailedStep string       `json:"failedStep,omitempty"`
	Seconds    float64      `json:"seconds"`
	Steps      []stepResult `json:"steps"`
	Restored   string       `json:"restored,omitempty"`
}

type step struct {
	name string
	run  func(context.Context, *Lab) (string, error)
}

type scenario struct {
	description string
	steps       []step
}

func waitStep(stage string, timeout time.Duration) step {
	return step{name: "wait:" + stage, run: func(ctx context.Context, l *Lab) (string, error) {
		result, err := l.waitOrFail(ctx, stage, timeout)
		if err != nil {
			return "", err
		}

		return result.Detail, nil
	}}
}

func scenarios() map[string]scenario {
	return map[string]scenario{
		ScenarioXID95: {
			description: "Inject XID 95 into the simulated GPU: detect, cordon, drain. Then clear it: " +
				"healthy, uncordon, and the workload runs on the node again.",
			steps: []step{
				waitStep(StageDCGMConnected, normalWait),
				waitStep(StageHealthy, shortWait),
				waitStep(StageUncordoned, shortWait),
				waitStep(StageWorkloadRunning, normalWait),
				{name: "inject_xid95", run: func(ctx context.Context, l *Lab) (string, error) {
					result, err := l.InjectFault(ctx, XID95)
					if err != nil {
						return "", err
					}

					return "pod " + result.Pod, nil
				}},
				waitStep(StageFaultDetected, normalWait),
				waitStep(StageCordoned, normalWait),
				waitStep(StageWorkloadEvicted, normalWait),
				waitStep(StageDrainSucceeded, normalWait),
				{name: "clear_fault", run: func(ctx context.Context, l *Lab) (string, error) {
					result, err := l.ClearFault(ctx)
					if err != nil {
						return "", err
					}

					return "deleted " + result["deleted"], nil
				}},
				waitStep(StageHealthy, longWait),
				waitStep(StageUncordoned, longWait),
				waitStep(StateCleared, normalWait),
				waitStep(StageWorkloadRunning, normalWait),
			},
		},
		ScenarioDCGMOutage: {
			description: "Take the simulated hostengine's Service away and restart it: the monitor reports it " +
				"lost DCGM. Restore it: the condition clears and the node is schedulable.",
			steps: []step{
				waitStep(StageDCGMConnected, normalWait),
				waitStep(StageUncordoned, shortWait),
				{name: "outage_on", run: func(ctx context.Context, l *Lab) (string, error) {
					return "", l.StartDCGMOutage(ctx)
				}},
				waitStep(StageDCGMUnreachable, normalWait),
				{name: "outage_off", run: func(ctx context.Context, l *Lab) (string, error) {
					return "", l.EndDCGMOutage(ctx)
				}},
				waitStep(StageDCGMConnected, longWait),
				waitStep(StageUncordoned, longWait),
			},
		},
	}
}

// Scenarios lists each scenario's name and description.
func Scenarios() map[string]string {
	out := map[string]string{}
	for name := range scenarios() {
		out[name] = scenarios()[name].description
	}

	return out
}

// ScenarioNames lists the scenario names.
func ScenarioNames() []string {
	names := make([]string, 0, len(scenarios()))
	for name := range scenarios() {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// OnStep hears about each step before it runs.
type OnStep func(index, total int, name string)

// RunScenario runs a scenario. If a step fails, ctx ends (the client left or the server
// is stopping) or a step panics, it resets the lab before returning. Only a SIGKILL or a
// power cut can leave a fault behind; the reset_lab tool undoes that.
func (l *Lab) RunScenario(ctx context.Context, name string, onStep OnStep) (*Report, error) {
	definition, ok := scenarios()[name]
	if !ok {
		return nil, failf("unknown scenario %q", name)
	}

	if err := l.RequireLab(ctx); err != nil {
		return nil, err
	}

	report := &Report{Scenario: name, Steps: []stepResult{}}
	start := time.Now()

	failed := l.runSteps(ctx, definition.steps, report, onStep)

	report.Seconds = time.Since(start).Round(100 * time.Millisecond).Seconds()
	report.Passed = !failed

	if failed {
		l.restore(ctx, report)
	}

	report.Seconds = time.Since(start).Round(100 * time.Millisecond).Seconds()

	return report, nil
}

func (l *Lab) runSteps(ctx context.Context, steps []step, report *Report, onStep OnStep) bool {
	for index, current := range steps {
		if ctx.Err() != nil {
			report.Steps = append(report.Steps, stepResult{Step: current.name, Error: stoppedMessage})
			report.FailedStep = current.name

			return true
		}

		if onStep != nil {
			onStep(index, len(steps), current.name)
		}

		result := runStep(ctx, l, current)
		report.Steps = append(report.Steps, result)

		if !result.OK {
			report.FailedStep = current.name
			return true
		}
	}

	return false
}

// runStep turns a panic into a failed step, so the reset still runs.
func runStep(ctx context.Context, l *Lab, current step) (result stepResult) {
	start := time.Now()
	result = stepResult{Step: current.name}

	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("a scenario step panicked", "step", current.name, "panic", fmt.Sprint(recovered))
			result.OK, result.Error = false, "the step crashed"
		}

		result.Seconds = time.Since(start).Round(100 * time.Millisecond).Seconds()
	}()

	detail, err := current.run(ctx, l)
	result.OK, result.Detail = err == nil, detail

	var userError *UserError

	switch {
	case err == nil:
	case errors.As(err, &userError):
		result.Error = userError.Error()
	case ctx.Err() != nil:
		result.Error = stoppedMessage
	default:
		slog.Error("a scenario step failed", "step", current.name, "error", err)

		result.Error = "the step failed unexpectedly"
	}

	return result
}

// restore runs on a context of its own: the one it was given may be why it runs.
func (l *Lab) restore(ctx context.Context, report *Report) {
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()

	if err := l.Reset(restoreCtx); err != nil {
		report.Restored = "failed: " + err.Error()
		return
	}

	report.Restored = "ok"
}
