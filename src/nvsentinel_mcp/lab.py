"""What the tools do, with no MCP in it: the CLI and the server both call this.

Reading works on the one cluster the configured kubeconfig points at. Changing
anything is off unless the operator turns it on, needs the kind lab (the guard in
Lab.require_lab), and injecting a fault also needs the DCGM pod to run the exact
simulated image (Lab.fake_dcgm_pod), so a real GPU's hostengine is never written
to. See docs/security.md.
"""

from __future__ import annotations

import json
import os
import re
import secrets
import shutil
import stat
import subprocess
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

LAB_CONTEXT = "kind-nvsentinel-lab"
DEFAULT_KUBECONFIG = "~/.kube/nvsentinel-lab-mcp.yaml"
ALLOW_CHANGES_ENV = "NVSENTINEL_MCP_ALLOW_CHANGES"
NVSENTINEL_NS = "nvsentinel"
DCGM_NS = "gpu-operator"
DCGM_SELECTOR = "app=nvidia-dcgm"
DCGM_SERVICE = "nvidia-dcgm"
# The one image a fault may be injected into, by digest: a look-alike tag fails.
FAKE_DCGM_IMAGE = (
    "ghcr.io/nvidia/nvsentinel-fake-dcgm:4.2.0@sha256:a0bf2a64410215f3661a0382cab22fb5e735eb903853d3dd3964471ba4830c94"  # noqa: E501
)
WORKLOAD_NS = "default"
WORKLOAD = "gpu-workload"
GPU_NODE_LABEL = "nvidia.com/gpu.present=true"
STATE_LABEL = "dgxc.nvidia.com/nvsentinel-state"
CONNECTIVITY_CONDITION = "GpuDcgmConnectivityFailure"

# DCGM_FI_DEV_XID_ERRORS. 95 is the upstream demo's: uncontained ECC, fatal,
# recommended action RESTART_VM.
XID_FIELD = 230
ALLOWED_XIDS = (95,)

# Past this, a node is drained. The labels go quarantined, draining, then these.
DRAINED_STATES = ("drain-succeeded", "remediating", "remediation-succeeded")

LOG_SOURCES = {
    "gpu-health-monitor": ["-l", "app.kubernetes.io/name=gpu-health-monitor"],
    "platform-connectors": ["-l", "app.kubernetes.io/name=nvsentinel"],
    "fault-quarantine": ["deployment/fault-quarantine"],
    "node-drainer": ["deployment/node-drainer"],
}
MAX_LOG_LINES = 2000
MAX_EVENTS = 200
MAX_WAIT_SECONDS = 900
MAX_OUTPUT_BYTES = 64 * 1024
MAX_ERROR_CHARS = 500
MAX_EVENT_MESSAGE_CHARS = 512

# Control characters and terminal escapes, keeping tab and newline.
_CONTROL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f-\x9f]")


class LabError(RuntimeError):
    """A command failed or a guard refused; the message says which."""


Runner = Callable[..., subprocess.CompletedProcess]


def clean(text: str, limit: int = MAX_OUTPUT_BYTES) -> str:
    """Strip control characters and keep the end of text that is too long."""
    text = _CONTROL.sub("", text)
    return text if len(text) <= limit else f"[truncated]\n{text[-limit:]}"


def fence(text: str) -> str:
    """Mark cluster output as data. The marker is random, so the content cannot close it."""
    marker = f"untrusted-cluster-data-{secrets.token_hex(8)}"
    return f"<{marker}>\n{clean(text)}\n</{marker}>"


class Lab:
    def __init__(
        self,
        kubeconfig: str | None = None,
        kubectl: str | None = None,
        runner: Runner = subprocess.run,
    ) -> None:
        path = kubeconfig or os.environ.get("NVSENTINEL_MCP_KUBECONFIG") or DEFAULT_KUBECONFIG
        self.kubeconfig = str(Path(path).expanduser())
        self.kubectl = kubectl or os.environ.get("NVSENTINEL_MCP_KUBECTL") or "kubectl"
        self.runner = runner
        self.pinned_context: str | None = None
        self.credentials_checked = False

    # ── kubectl ──────────────────────────────────────────────────────────
    def resolve_kubectl(self) -> str:
        found = shutil.which(self.kubectl)
        if found is None:
            raise LabError(f"{self.kubectl} was not found")
        return found

    def check_kubeconfig(self) -> None:
        """The credentials must exist and be private. Only this file is ever used."""
        try:
            info = os.stat(self.kubeconfig)
        except OSError as err:
            raise LabError(f"no kubeconfig at the configured path: {err.strerror}") from err
        if not stat.S_ISREG(info.st_mode):
            raise LabError("the configured kubeconfig is not a regular file")
        if info.st_mode & 0o077:
            raise LabError("refusing: the kubeconfig is readable by others; chmod 600 it")

    def check_credentials(self) -> None:
        """Refuse a credential plugin, which would run a command, before kubectl talks to any server."""
        self.credentials_checked = True
        config = json.loads(self.run("config", "view", "--minify", "-o", "json"))
        for user in config.get("users", []):
            settings = user.get("user") or {}
            if "exec" in settings or "auth-provider" in settings:
                self.credentials_checked = False
                raise LabError("refusing: the kubeconfig runs a credential plugin; it can only use a token or certificate")

    def run(self, *args: str, timeout: int = 60) -> str:
        """Run kubectl from an argument list (no shell) with a minimal environment."""
        self.check_kubeconfig()
        if not self.credentials_checked:
            self.check_credentials()
        command = [self.resolve_kubectl(), "--kubeconfig", self.kubeconfig]
        if self.pinned_context:
            command += ["--context", self.pinned_context]
        command += args
        # No KUBECONFIG or proxy settings from the parent's environment.
        environment = {
            "PATH": os.defpath,
            "HOME": os.environ.get("HOME", "/tmp"),  # noqa: S108
            "KUBECACHEDIR": os.environ.get("KUBECACHEDIR", "/tmp/nvsentinel-mcp-kube-cache"),  # noqa: S108
        }
        try:
            done = self.runner(command, capture_output=True, text=True, timeout=timeout, env=environment)
        except subprocess.TimeoutExpired as err:
            raise LabError(f"kubectl {args[0]} did not finish in {timeout}s") from err
        if done.returncode != 0:
            raise LabError(f"kubectl {' '.join(args[:3])}: {clean(done.stderr, MAX_ERROR_CHARS).strip()}")
        return done.stdout

    def get(self, *args: str) -> dict[str, Any]:
        return json.loads(self.run("get", *args, "-o", "json"))

    # ── Guards ───────────────────────────────────────────────────────────
    def require_lab(self) -> None:
        """Refuse to change anything unless turned on, and unless this is the kind lab.

        Passing also pins the context for the rest of this object's calls, so a
        kubeconfig edited after the check cannot redirect them.
        """
        if os.environ.get(ALLOW_CHANGES_ENV) != "true":
            raise LabError(f"refusing: changes are off; the operator sets {ALLOW_CHANGES_ENV}=true to allow them")
        context = self.run("config", "current-context").strip()
        if context != LAB_CONTEXT:
            raise LabError(f"refusing: the context is {context!r}, not the lab's {LAB_CONTEXT!r}")
        self.pinned_context = LAB_CONTEXT
        nodes = self.get("nodes")["items"]
        if not nodes:
            raise LabError("refusing: the cluster has no nodes")
        for node in nodes:
            provider = node["spec"].get("providerID", "")
            if not provider.startswith("kind://"):
                raise LabError(f"refusing: node {node['metadata']['name']} is not a kind node")

    def gpu_node(self) -> str:
        nodes = self.get("nodes", "-l", GPU_NODE_LABEL)["items"]
        if len(nodes) != 1:
            raise LabError(f"expected one node labelled {GPU_NODE_LABEL}, found {len(nodes)}")
        return nodes[0]["metadata"]["name"]

    def fake_dcgm_pod(self, node: str) -> str:
        """The DCGM pod on the node, only if every container in it runs the pinned simulated image."""
        pods = self.get("pods", "-n", DCGM_NS, "-l", DCGM_SELECTOR, "--field-selector", f"spec.nodeName={node}")
        running = [p for p in pods["items"] if not p["metadata"].get("deletionTimestamp")]
        if not running:
            raise LabError(f"no DCGM pod on {node}")
        pod = running[0]
        spec = pod["spec"]
        containers = [*spec.get("containers", []), *spec.get("initContainers", []), *spec.get("ephemeralContainers", [])]
        if not containers or any(c.get("image") != FAKE_DCGM_IMAGE for c in containers):
            raise LabError("refusing: the DCGM pod does not run only the pinned simulated image")
        return pod["metadata"]["name"]

    # ── Reading ──────────────────────────────────────────────────────────
    def status(self) -> dict[str, Any]:
        nodes = []
        for node in self.get("nodes")["items"]:
            conditions = {c["type"]: c["status"] for c in node["status"].get("conditions", [])}
            nodes.append(
                {
                    "name": node["metadata"]["name"],
                    "ready": conditions.get("Ready") == "True",
                    "cordoned": bool(node["spec"].get("unschedulable")),
                    "state": node["metadata"].get("labels", {}).get(STATE_LABEL),
                    "dcgm_connectivity_failure": conditions.get(CONNECTIVITY_CONDITION),
                    "failing_checks": sorted(k for k, v in conditions.items() if k.startswith("Gpu") and v == "True"),
                    "gpu_checks_reported": sum(1 for k in conditions if k.startswith("Gpu")),
                }
            )
        return {
            "context": self.run("config", "current-context").strip(),
            "changes_allowed": os.environ.get(ALLOW_CHANGES_ENV) == "true",
            "nodes": nodes,
            "pods": self._pods(NVSENTINEL_NS),
            "dcgm_pods": self._pods(DCGM_NS),
            "workload": self._pods(WORKLOAD_NS, "-l", f"app={WORKLOAD}"),
        }

    def _pods(self, namespace: str, *selector: str) -> list[dict[str, Any]]:
        out = []
        for pod in self.get("pods", "-n", namespace, *selector)["items"]:
            statuses = pod["status"].get("containerStatuses", [])
            out.append(
                {
                    "name": pod["metadata"]["name"],
                    "node": pod["spec"].get("nodeName"),
                    "phase": pod["status"].get("phase"),
                    "ready": bool(statuses) and all(s.get("ready") for s in statuses),
                    "restarts": sum(s.get("restartCount", 0) for s in statuses),
                }
            )
        return out

    def logs(self, component: str, tail: int = 100) -> str:
        source = LOG_SOURCES.get(component)
        if source is None:
            raise LabError(f"unknown component {component!r}; one of {sorted(LOG_SOURCES)}")
        lines = max(1, min(int(tail), MAX_LOG_LINES))
        limits = [f"--tail={lines}", f"--limit-bytes={MAX_OUTPUT_BYTES}"]
        output = self.run("logs", "-n", NVSENTINEL_NS, *source, *limits, "--all-containers", "--prefix")
        return fence(output)

    def node_events(self, limit: int = 20) -> str:
        node = self.gpu_node()
        selector = f"involvedObject.kind=Node,involvedObject.name={node}"
        events = self.get("events", "-A", "--field-selector", selector)["items"]
        events.sort(key=lambda e: e.get("lastTimestamp") or e.get("eventTime") or "")
        kept = events[-max(1, min(int(limit), MAX_EVENTS)) :]
        rows = [
            {
                "time": e.get("lastTimestamp") or e.get("eventTime"),
                "reason": e.get("reason"),
                "message": clean(e.get("message") or "", MAX_EVENT_MESSAGE_CHARS),
            }
            for e in kept
        ]
        return fence("\n".join(json.dumps(row) for row in rows))

    # ── Stages: is the cluster there yet? ────────────────────────────────
    def stage(self, name: str) -> tuple[bool, str]:
        check = STAGES.get(name)
        if check is None:
            raise LabError(f"unknown stage {name!r}; one of {sorted(STAGES)}")
        return check(self)

    def wait_for(self, name: str, timeout: int = 180, poll: float = 3.0) -> dict[str, Any]:
        """Poll a stage. A failed query is not progress: it is reported, never read as done."""
        if name not in STAGES:
            raise LabError(f"unknown stage {name!r}; one of {sorted(STAGES)}")
        limit = max(0, min(int(timeout), MAX_WAIT_SECONDS))
        start = time.monotonic()
        detail = ""
        while True:
            try:
                reached, detail = self.stage(name)
            except LabError as err:
                reached, detail = False, f"query failed: {err}"
            elapsed = round(time.monotonic() - start, 1)
            if reached:
                return {"stage": name, "reached": True, "seconds": elapsed, "detail": detail}
            if elapsed >= limit:
                return {"stage": name, "reached": False, "seconds": elapsed, "detail": detail}
            time.sleep(poll)

    def _node(self) -> dict[str, Any]:
        return self.get("node", self.gpu_node())

    def _conditions(self) -> dict[str, str]:
        return {c["type"]: c["status"] for c in self._node()["status"].get("conditions", [])}

    def _workload_nodes(self) -> list[str]:
        pods = self.get("pods", "-n", WORKLOAD_NS, "-l", f"app={WORKLOAD}")["items"]
        return [p["spec"]["nodeName"] for p in pods if p["spec"].get("nodeName")]

    # ── Changing the lab ─────────────────────────────────────────────────
    def inject_fault(self, xid: int = 95) -> dict[str, Any]:
        if xid not in ALLOWED_XIDS:
            raise LabError(f"XID {xid} is not allowed; one of {list(ALLOWED_XIDS)}")
        self.require_lab()
        node = self.gpu_node()
        pod = self.fake_dcgm_pod(node)
        output = self.run(
            "exec", "-n", DCGM_NS, pod, "--",
            "dcgmi", "test", "--inject", "--gpuid", "0", "-f", str(XID_FIELD), "-v", str(xid),
        )  # fmt: skip
        return {"node": node, "pod": pod, "xid": xid, "output": clean(output, MAX_ERROR_CHARS).strip()}

    def clear_fault(self, timeout: int = 180) -> dict[str, Any]:
        """Restart the simulated hostengine: the fault lives in it, so this stands in for a reboot."""
        self.require_lab()
        node = self.gpu_node()
        old = self.fake_dcgm_pod(node)
        self.run("delete", "pod", "-n", DCGM_NS, old, "--wait=true", timeout=timeout)
        result = self.wait_for("dcgm_pod_ready", timeout=timeout)
        return {"node": node, "deleted": old, **result}

    def set_dcgm_outage(self, down: bool) -> dict[str, Any]:
        """Point the DCGM Service at nothing (or back), so the monitor loses its hostengine."""
        self.require_lab()
        self.fake_dcgm_pod(self.gpu_node())
        selector = {"app": "nvidia-dcgm-outage" if down else "nvidia-dcgm"}
        patch = json.dumps({"spec": {"selector": selector}})
        self.run("patch", "service", DCGM_SERVICE, "-n", DCGM_NS, "--type=merge", "-p", patch)
        return {"service": DCGM_SERVICE, "down": down}


def _dcgm_connected(lab: Lab) -> tuple[bool, str]:
    value = lab._conditions().get(CONNECTIVITY_CONDITION)
    return value == "False", f"{CONNECTIVITY_CONDITION}={value}"


def _dcgm_unreachable(lab: Lab) -> tuple[bool, str]:
    value = lab._conditions().get(CONNECTIVITY_CONDITION)
    return value == "True", f"{CONNECTIVITY_CONDITION}={value}"


def _dcgm_pod_ready(lab: Lab) -> tuple[bool, str]:
    pods = lab.get("pods", "-n", DCGM_NS, "-l", DCGM_SELECTOR)["items"]
    live = [p for p in pods if not p["metadata"].get("deletionTimestamp")]
    ready = [p for p in live if any(c["type"] == "Ready" and c["status"] == "True" for c in p["status"].get("conditions", []))]
    return bool(ready), f"{len(ready)} of {len(live)} DCGM pods ready"


def _fault_detected(lab: Lab) -> tuple[bool, str]:
    failing = [k for k, v in lab._conditions().items() if k.startswith("Gpu") and v == "True" and k != CONNECTIVITY_CONDITION]
    return bool(failing), f"failing: {failing}"


def _healthy(lab: Lab) -> tuple[bool, str]:
    conditions = lab._conditions()
    reported = [k for k in conditions if k.startswith("Gpu")]
    failing = [k for k in reported if conditions[k] == "True"]
    return bool(reported) and not failing, f"{len(reported)} checks reported, failing: {failing}"


def _cordoned(lab: Lab) -> tuple[bool, str]:
    value = bool(lab._node()["spec"].get("unschedulable"))
    return value, f"unschedulable={value}"


def _uncordoned(lab: Lab) -> tuple[bool, str]:
    value = bool(lab._node()["spec"].get("unschedulable"))
    return not value, f"unschedulable={value}"


def _workload_evicted(lab: Lab) -> tuple[bool, str]:
    node = lab.gpu_node()
    nodes = lab._workload_nodes()
    return node not in nodes, f"workload pods on {nodes}"


def _drain_succeeded(lab: Lab) -> tuple[bool, str]:
    state = lab._node()["metadata"].get("labels", {}).get(STATE_LABEL)
    return state in DRAINED_STATES, f"state={state}"


def _state_cleared(lab: Lab) -> tuple[bool, str]:
    state = lab._node()["metadata"].get("labels", {}).get(STATE_LABEL)
    return state is None, f"state={state}"


def _workload_running(lab: Lab) -> tuple[bool, str]:
    deployment = lab.get("deployment", WORKLOAD, "-n", WORKLOAD_NS)
    ready = deployment["status"].get("readyReplicas", 0)
    on_node = lab.gpu_node() in lab._workload_nodes()
    return ready >= 1 and on_node, f"readyReplicas={ready}, on the GPU node: {on_node}"


STAGES: dict[str, Callable[[Lab], tuple[bool, str]]] = {
    "dcgm_connected": _dcgm_connected,
    "dcgm_unreachable": _dcgm_unreachable,
    "dcgm_pod_ready": _dcgm_pod_ready,
    "fault_detected": _fault_detected,
    "healthy": _healthy,
    "cordoned": _cordoned,
    "uncordoned": _uncordoned,
    "workload_evicted": _workload_evicted,
    "drain_succeeded": _drain_succeeded,
    "state_cleared": _state_cleared,
    "workload_running": _workload_running,
}
