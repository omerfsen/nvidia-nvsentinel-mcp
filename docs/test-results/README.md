# Example test results

Written by `make e2e`: the real server binary, spoken to over MCP on stdio, as an MCP client
would. They are examples of what the tools return, not a guarantee for other versions.

| | |
|---|---|
| Cluster | kind, 2 nodes, Kubernetes v1.36.4 |
| NVSentinel | v1.30.0, installed by omerfsen.aicr (quarantine step: detect, cordon, drain) |
| cert-manager | v1.21.3 |
| GPU | None: `ghcr.io/nvidia/nvsentinel-fake-dcgm:4.2.0` |
| The server's identity | Its own ServiceAccount (`lab/rbac.yaml`), not an admin |

**These prove NVSentinel's pipeline against simulated DCGM, not real GPU health.** Reboot and GPU
reset did not run: clearing a fault means restarting the simulated hostengine.

| File | What it shows |
|---|---|
| [xid95.json](xid95.json) | XID 95 injected: detected, cordoned, workload evicted, drain succeeded. Cleared: healthy, uncordoned, workload back |
| [dcgm_outage.json](dcgm_outage.json) | The hostengine taken away: `GpuDcgmConnectivityFailure=True`. Restored: `False` |
| [status.json](status.json) | `lab_status` afterwards, with changes off |
| [refused-changes-off.json](refused-changes-off.json) | `inject_fault` refused, as changes are off |
| [refused-other-context.json](refused-other-context.json) | `inject_fault` refused with changes on, as the same credentials under another context name are not the lab |

To run them yourself, see [the README](../../README.md#run-the-tests-on-kind).
