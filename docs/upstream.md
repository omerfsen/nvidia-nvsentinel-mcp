# Taking this to NVSentinel

How to turn this repo into a PR to https://github.com/NVIDIA/NVSentinel. Nothing here has been
proposed to NVIDIA, and they may want it in another shape, or not at all.

## Before any code

1. **Open an issue first.** `CONTRIBUTING.md` asks for one before anything non-trivial, and its
   text can come from the design record below.
2. **Propose the design record.** `AGENTS.md` wants an ADR for a new module, before it is built.
   [adr-draft.md](adr-draft.md) follows their template. A maintainer owns the decision.

## What already matches

| Their rule | Here |
|---|---|
| Go 1.27.3, one module per component, `main.go` and `pkg/` | Same layout |
| `client-go` v0.37.2 and `stretchr/testify` | Same versions |
| `.golangci.yml` | A copy of theirs from v1.30.0; `make lint` is clean, tests are not linted |
| Wrap errors with `%w`, `context.Context` on every I/O path | Yes |
| A test proves remediation does not fire when healthy | `TestAHealthyNodeReachesNoFaultStage` |
| Conventional Commit PR title | Yes |
| No AI attribution trailers | Yes |
| Secrets read at run time | Yes |

## What NVIDIA's own MCP server does

NICo's `nico-mcp` (`dsx-ai-factory/infra-controller` v2.3.0, `rest-api/mcp`,
`docs/operations/nico-mcp.md`) is the nearest example. This is what its code and docs show:

| `nico-mcp` | This server | Why they differ |
|---|---|---|
| Go, official MCP SDK, `jsonschema-go` | The same | |
| Read-only, by construction: only `GET` operations become tools | Reads, plus change tools that are off by default | Testing needs faults, on a throwaway lab only |
| Tools marked `ReadOnlyHint` | Tools marked read-only or destructive | |
| Streamable HTTP in the cluster, with a Helm chart | stdio on the operator's machine, no chart | A long-lived shared service against a local test tool |
| Does not authenticate: needs a frontend for auth and TLS, which it does not ship | No listener, so nothing to authenticate | |
| A startup base URL pins the destination; a tool argument cannot change it | No tool argument can choose a cluster | Same principle, stricter here |
| Credentials are never sent to a destination the caller chose | Same: one kubeconfig, set by the operator | |
| Does not read `~/.nico/config.yaml` | Does not read `~/.kube/config` or `KUBECONFIG` | Same principle |
| Tokens not exposed as chart values | No tokens in tool arguments at all | `nico-mcp` also accepts a per-call token, which puts it in the LLM's context |

If upstream wants this over HTTP, `nico-mcp` is the pattern to follow, and it would then need
what its docs list: authentication, TLS and network limits in front of it.

NVIDIA also publishes `io.github.NVIDIA/elements` on the official MCP Registry, and ships
[SkillSpector](https://github.com/NVIDIA/SkillSpector), a scanner for agent skills.

## What changes in the PR

| Change | Why |
|---|---|
| Move to a new top-level module such as `mcp-server/` | Their modules sit at the root |
| Module path `github.com/nvidia/nvsentinel/mcp-server` | Their naming |
| Add it to `go.work`, `.ko.yaml`, the root `Makefile` and CI | `make lint-test-all` must see it |
| Build with `ko`, and drop the `Dockerfile` | Go images have no Dockerfile there |
| Use their `commons/pkg/logger` instead of plain `slog` | Their pattern |
| Copyright headers | Decide with them: their files carry an NVIDIA header. `make license-headers-lint` must pass |
| Every commit signed off: `git commit -s` | DCO is enforced |
| Say the Go MCP SDK and `jsonschema-go` are new dependencies | `AGENTS.md` asks for it; this ships to customers |
| Run `make lint-test-all` | Their gate, and it has not been run against this code |

The skill can move too: upstream already keeps skills under `skills/` (`skills/mongodb-migration`).

## What stays out

- `lab/` and its AICR install: their `demos/local-fault-injection-demo` already builds the same
  kind cluster, fake DCGM and workload. Set `NVSENTINEL_MCP_LAB_CONTEXT=kind-nvsentinel-demo` and
  use a kubeconfig for it. The names match by reading both, but **this has not been run**. It also
  needs the RBAC, or an admin kubeconfig on a throwaway cluster. The demo's `kind` provider adds a
  simulated `RebootNode` that no scenario here checks yet.
- `docs/test-results`, `docs/build-log.md`, `release-please` and the cicd workflows.
- `.mcp.json`, unless they want one.

## Questions for them

- Is an MCP server in scope at all, or better as a separate project they link to?
- Should it stay test-only, or also read production clusters? Reading works today, but this code
  was designed for a lab.
- Is a single allowed XID enough, or should the list follow their tests?
- `dcgm_outage` depends on `GpuDcgmConnectivityFailure`. Is that condition's name a stable API?
