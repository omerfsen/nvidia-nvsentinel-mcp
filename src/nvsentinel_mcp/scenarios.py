"""End-to-end scenarios: each step waits on the effect it should produce, not on the clock.

A scenario stops at the first step that fails and reports what it saw. It still
runs its restore steps, so a failed run does not leave the fault in the lab.
"""

from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any

from .lab import Lab, LabError

Step = tuple[str, Callable[[Lab], dict[str, Any]]]
OnStep = Callable[[int, int, str], None]


def _wait(stage: str, timeout: int = 180) -> Step:
    def run(lab: Lab) -> dict[str, Any]:
        result = lab.wait_for(stage, timeout=timeout)
        if not result["reached"]:
            raise LabError(f"{stage} not reached in {timeout}s: {result['detail']}")
        return result

    return (f"wait:{stage}", run)


def _do(name: str, action: Callable[[Lab], dict[str, Any]]) -> Step:
    return (name, action)


@dataclass(frozen=True)
class Scenario:
    description: str
    steps: list[Step]
    restore: list[Step] = field(default_factory=list)


SCENARIOS: dict[str, Scenario] = {
    "xid95": Scenario(
        description=(
            "Inject XID 95 into the simulated GPU: detect, cordon, drain. Then clear it: "
            "healthy, uncordon, the workload runs on the node again."
        ),
        steps=[
            _wait("dcgm_connected", 120),
            _wait("healthy", 60),
            _wait("uncordoned", 60),
            _wait("workload_running", 120),
            _do("inject_xid95", lambda lab: lab.inject_fault(95)),
            _wait("fault_detected"),
            _wait("cordoned"),
            _wait("workload_evicted"),
            _wait("drain_succeeded"),
            _do("clear_fault", lambda lab: lab.clear_fault()),
            _wait("healthy", 300),
            _wait("uncordoned", 300),
            _wait("state_cleared", 120),
            _wait("workload_running", 180),
        ],
        restore=[_do("clear_fault", lambda lab: lab.clear_fault())],
    ),
    "dcgm_outage": Scenario(
        description=(
            "Take the simulated hostengine's Service away: the monitor reports it lost DCGM. "
            "Restore it: the condition clears and the node is schedulable."
        ),
        steps=[
            _wait("dcgm_connected", 120),
            _wait("uncordoned", 60),
            _do("outage_on", lambda lab: lab.set_dcgm_outage(True)),
            _wait("dcgm_unreachable"),
            _do("outage_off", lambda lab: lab.set_dcgm_outage(False)),
            _wait("dcgm_connected", 300),
            _wait("uncordoned", 300),
        ],
        restore=[_do("outage_off", lambda lab: lab.set_dcgm_outage(False))],
    ),
}


def run_scenario(lab: Lab, name: str, on_step: OnStep | None = None) -> dict[str, Any]:
    scenario = SCENARIOS.get(name)
    if scenario is None:
        raise LabError(f"unknown scenario {name!r}; one of {sorted(SCENARIOS)}")
    lab.require_lab()

    report: list[dict[str, Any]] = []
    failed: str | None = None
    start = time.monotonic()
    for index, (step_name, action) in enumerate(scenario.steps):
        if on_step:
            on_step(index, len(scenario.steps), step_name)
        step_start = time.monotonic()
        try:
            result = action(lab)
            report.append({"step": step_name, "ok": True, "seconds": round(time.monotonic() - step_start, 1), "result": result})
        except LabError as err:
            report.append({"step": step_name, "ok": False, "seconds": round(time.monotonic() - step_start, 1), "error": str(err)})
            failed = step_name
            break

    restored: list[dict[str, Any]] = []
    if failed:
        for step_name, action in scenario.restore:
            try:
                restored.append({"step": step_name, "ok": True, "result": action(lab)})
            except LabError as err:
                restored.append({"step": step_name, "ok": False, "error": str(err)})

    return {
        "scenario": name,
        "passed": failed is None,
        "failed_step": failed,
        "seconds": round(time.monotonic() - start, 1),
        "steps": report,
        "restore": restored,
    }
