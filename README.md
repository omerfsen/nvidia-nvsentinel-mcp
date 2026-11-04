# nvidia-nvsentinel-mcp

An MCP server for testing [NVIDIA NVSentinel](https://github.com/NVIDIA/NVSentinel)
(GPU fault detection and remediation on Kubernetes) on a lab cluster, written in Go
like NVSentinel itself. An MCP client such as Claude Code can read what NVSentinel
reports, inject a fault into a simulated GPU, and check that NVSentinel cordons and
drains the node.

This repo holds the server and example test results. It does not install NVSentinel:
that is [omerfsen.aicr](https://github.com/omerfsen/aicr), on a cluster from
[omerfsen.nvke](https://github.com/omerfsen/nvke) or on kind. It is meant to go
upstream one day: see [docs/upstream.md](docs/upstream.md).

## Test results

Real output from NVSentinel v1.30.0 on kind, through this server over MCP:
[docs/test-results](docs/test-results/README.md). **These prove NVSentinel's pipeline
against simulated DCGM, not real GPU health.**

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
| `reset_lab` | yes | Undoes a DCGM outage and an injected fault |
| `run_test_scenario` | yes | Runs a scenario end to end and reports each step |

| Scenario | What it checks |
|---|---|
| `xid95` | Inject XID 95: detected, cordoned, workload evicted, drain succeeded. Clear it: healthy, uncordoned, workload back |
| `dcgm_outage` | Take the hostengine away: the monitor reports it lost DCGM. Restore it: the condition clears |

Tools that change anything are off unless `NVSENTINEL_MCP_ALLOW_CHANGES=true`, and
then work only on a kind cluster. Details: [docs/security.md](docs/security.md).

## Install

The server needs a kubeconfig. For the kind lab, `make lab-token` writes one (see
[Run the tests on kind](#run-the-tests-on-kind)). It speaks MCP over stdio, so any MCP client
can start it. All the commands below leave changes off; add
`-e NVSENTINEL_MCP_ALLOW_CHANGES=true` only for a client that should change the lab.

**1. Install the server**, as a binary (needs Go 1.27):

```shell
go install github.com/omerfsen/nvidia-nvsentinel-mcp@latest
```

The binary is `nvidia-nvsentinel-mcp`, in `$(go env GOPATH)/bin`. Or use the image, once it is
published to Docker Hub.

**2. Add it to your client.** For Claude Code:

```shell
claude mcp add nvsentinel-lab -- nvidia-nvsentinel-mcp
```

With the image:

```shell
claude mcp add nvsentinel-lab -- docker run --rm -i --read-only --cap-drop=ALL \
  --security-opt=no-new-privileges --user "$(id -u):$(id -g)" --memory=128m --cpus=1 \
  --pids-limit=64 --network=kind \
  --volume "$HOME/.kube/nvsentinel-lab-mcp-docker.yaml:/kube/config:ro" \
  docker.io/omerfsen/nvidia-nvsentinel-mcp@sha256:<digest>
```

Other clients take the same command in their MCP config:

```json
{ "mcpServers": { "nvsentinel-lab": { "command": "nvidia-nvsentinel-mcp" } } }
```

**3. Optional: add the skill**, which teaches an agent how to use the tools. It holds only
instructions, so step 1 and 2 are still needed. Either command works:

```shell
gh skill install omerfsen/nvidia-nvsentinel-mcp nvsentinel-lab
npx skills add omerfsen/nvidia-nvsentinel-mcp --skill nvsentinel-lab
```

The GitHub CLI has no `gh mcp add` as of v2.103.2: `gh skill` installs skills, and MCP servers
go in through the client. The official MCP Registry entry is ready in `server.json` but not
published, as the image must exist first.

## Run the tests on kind

Needs Go, Docker, kubectl, helm, yq, about 10 GiB of free memory, and a checkout of
[omerfsen/aicr](https://github.com/omerfsen/aicr) with `make dev` done (until
https://github.com/omerfsen/aicr/pull/8 merges, its `dev/osen/nvsentinel-latest` branch).

```shell
make lab-up           # the kind cluster, with its own kubeconfig
make lab-install      # NVSentinel v1.30.0 through omerfsen.aicr (AICR_DIR=<checkout>)
make lab-fixture      # simulated DCGM, a labelled node, a workload, the server's RBAC
make lab-token        # a kubeconfig for the server: its own identity, valid 8 hours
make e2e              # the tests, through the real binary over MCP; writes docs/test-results
make image e2e-image  # the same, with the server in its container
make lab-down
```

Tests with no cluster: `make lint test`.

## Use it from Claude Code

From a checkout, for working on the server itself: `make build lab-token`, then restart Claude
Code in this directory and approve the server from `.mcp.json`. That one allows changes.

## Settings

| Variable | Default | What it does |
|---|---|---|
| `NVSENTINEL_MCP_KUBECONFIG` | `~/.kube/nvsentinel-lab-mcp.yaml` (image: `/kube/config`) | The one kubeconfig it uses. Must be private (`chmod 600`) |
| `NVSENTINEL_MCP_ALLOW_CHANGES` | off | Exactly `true` allows the change tools, on the lab only |
| `NVSENTINEL_MCP_LAB_CONTEXT` | `kind-nvsentinel-lab` | The context changes need. Must start with `kind-` |

## What is not tested

- A real GPU. The RTX 4080 SUPER is not on NVSentinel's validated list, and real DCGM
  health is still to be checked.
- AICR's install on NVKE, as opposed to kind.
- Remediation. The kind install stops at cordon and drain: AICR's remediation mixin
  needs metadata-collector, which needs NVML.
- The syslog monitor: it reads kernel-transport journal entries, which a kind node
  cannot safely produce, so it is off on kind.
- Pushing the image, signing it, or its attestations.
- `make test` in CI: cicd has no Go test pipeline yet.

Every step, failure and dead end is in [docs/build-log.md](docs/build-log.md).

## License

Apache-2.0. `lab/gpu-spec.yaml` and `lab/fake-dcgm.yaml` come from NVSentinel, under
the same license.
