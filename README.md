# nvidia-nvsentinel-mcp

An MCP server for testing [NVIDIA NVSentinel](https://github.com/NVIDIA/NVSentinel)
(GPU fault detection and remediation on Kubernetes) on a lab cluster. An MCP client
such as Claude Code can read what NVSentinel reports, inject a fault into a simulated
GPU, and check that NVSentinel cordons and drains the node.

**Status: not yet run against a cluster.** The unit and protocol tests pass with a
fake kubectl, and the image builds and answers MCP. The lab scripts and `make e2e` have
never run; see [what is not tested](#what-is-not-tested).

This repo does not install NVSentinel. That is
[omerfsen.aicr](https://github.com/omerfsen/aicr) on a cluster from
[omerfsen.nvke](https://github.com/omerfsen/nvke), or the kind example below.

## Tools

| Tool | Changes the lab | What it does |
|---|---|---|
| `lab_status` | no | Nodes (cordon, NVSentinel state, failing GPU checks), pods, the workload |
| `list_scenarios` | no | Scenarios, stages, allowed XIDs, log sources |
| `wait_for_stage` | no | Polls until a stage holds, such as `cordoned` |
| `component_logs` | no | The last lines of a NVSentinel component's logs |
| `node_events` | no | Recent events on the GPU node |
| `inject_fault` | yes | Injects XID 95 into the simulated GPU |
| `clear_fault` | yes | Restarts the simulated hostengine, which drops the fault |
| `run_test_scenario` | yes | Runs a scenario end to end and reports each step |

| Scenario | What it checks |
|---|---|
| `xid95` | Inject XID 95: detected, cordoned, workload evicted, drain succeeded. Clear it: healthy, uncordoned, workload back |
| `dcgm_outage` | Take the hostengine's Service away: the monitor reports it. Restore it: the condition clears |

Tools that change anything are off unless `NVSENTINEL_MCP_ALLOW_CHANGES=true`, and
then work only on the kind lab. **A passed scenario proves NVSentinel's pipeline
against simulated DCGM, not real GPU health.** Details: [docs/security.md](docs/security.md).

## Run the tests on kind

Needs Docker, kubectl, helm, about 10 GiB of free memory, and a checkout of
[omerfsen/aicr](https://github.com/omerfsen/aicr) with `make dev` done (until
https://github.com/omerfsen/aicr/pull/8 merges, its `dev/osen/nvsentinel-latest` branch).

```shell
make dev              # on the build machine: make dev PYTHON=/home/linuxbrew/.linuxbrew/opt/python@3.12/bin/python3.12
make lab-up           # the kind cluster, with its own kubeconfig
make lab-install      # NVSentinel v1.30.0 through omerfsen.aicr (AICR_DIR=<checkout>)
make lab-fixture      # simulated DCGM, a labelled node, a workload, the server's RBAC
make lab-token        # a kubeconfig for the server: its own identity, valid 8 hours
make e2e              # the scenarios, through the MCP server
make lab-down
```

Tests with no cluster: `make lint test`.

## Use it from Claude Code

`.mcp.json` registers the server from this checkout, using the kubeconfig from
`make lab-token`. Restart Claude Code in this directory and approve the server. Or use
the image, with the flags in [docs/security.md](docs/security.md#controls).

## Settings

| Variable | Default | What it does |
|---|---|---|
| `NVSENTINEL_MCP_KUBECONFIG` | `~/.kube/nvsentinel-lab-mcp.yaml` (image: `/kube/config`) | The one kubeconfig it uses. Must be private (`chmod 600`) |
| `NVSENTINEL_MCP_ALLOW_CHANGES` | off | Exactly `true` allows the change tools, on the kind lab only |
| `NVSENTINEL_MCP_KUBECTL` | `kubectl` (image: its own) | The kubectl to run |

## What is not tested

- Anything on a real cluster: the kind lab, `lab/` scripts, RBAC and scenarios.
- The AICR install on kind or NVKE (rendered only).
- A real GPU. The RTX 4080 SUPER is not on NVSentinel's validated list, and real DCGM
  health is still to be checked.
- Remediation. The kind install stops at cordon and drain: AICR's remediation mixin
  needs metadata-collector, which needs NVML.
- Pushing the image, signing it, or its attestations.

Every step, failure and dead end is in [docs/build-log.md](docs/build-log.md).

## License

Apache-2.0. `lab/gpu-spec.yaml` is from NVSentinel, under the same license.
