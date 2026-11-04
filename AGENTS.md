# AGENTS.md

Guidance for coding agents and people working in this repo. `CLAUDE.md` imports this file, so
this is the only copy. [README.md](README.md) covers using the server.

## What this repo is

An MCP server, in Go, for testing [NVIDIA NVSentinel](https://github.com/NVIDIA/NVSentinel) on a
lab cluster, and example test results. An MCP client reads what NVSentinel reports, injects a fault
into a simulated GPU, and checks that NVSentinel cordons and drains the node.

It does **not** install NVSentinel: that is [omerfsen.aicr](https://github.com/omerfsen/aicr), on a
cluster from [omerfsen.nvke](https://github.com/omerfsen/nvke) or on kind. Do not add Ansible, Helm
charts or Python here. It is meant to go upstream one day: see [docs/upstream.md](docs/upstream.md).

## How it works

```text
MCP client  --stdio-->  pkg/server  --calls-->  pkg/lab  --client-go-->  Kubernetes API
                        tools, schemas,         cluster logic, checks,   nothing else: no shell,
                        lock, audit             scenarios, stages        no kubectl, no listener
```

| Path | What it holds |
|---|---|
| `main.go` | Flags, logging, signals, shutdown |
| `pkg/server/server.go` | The tools, their schemas, the one-change lock, the audit log |
| `pkg/lab/lab.go` | Status, logs, events, faults, `RequireLab`, `LogComponents`, `AllowedXIDs` |
| `pkg/lab/stages.go` | Stages `wait_for_stage` can wait on |
| `pkg/lab/scenarios.go` | Scenarios, and the reset that runs when one fails or is stopped |
| `pkg/lab/kubeconfig.go` | Settings, and which kubeconfigs are refused |
| `pkg/lab/output.go`, `exec.go` | Errors, cleaning and fencing cluster text, capped websocket exec |
| `lab/` | Scripts and manifests for the kind lab, including the server's RBAC |
| `tests/e2e/` | Tests through the real binary or image, over MCP, on the lab |
| `docs/tools.md`, `docs/tools.json` | **Generated:** never edit them by hand |
| `docs/test-results/` | Real results, written by `make e2e` |
| `docs/security.md` | Threat model; each control names the test that proves it |
| `docs/mcp-standards.md` | How the server compares with the MCP standards, and how to re-check |
| `docs/build-log.md` | Every step, failure and dead end |

## Commands

```shell
make build lint test   # no cluster needed; run all three before a PR
make docs              # regenerate docs/tools.md and docs/tools.json
make lab-up lab-install lab-fixture lab-token   # the kind lab (lab-install needs AICR_DIR=<checkout>)
make e2e               # the tests through the binary; writes docs/test-results
make image e2e-image   # the same through the image
make lab-down
```

CI runs `go vet`, `go test -race`, the image build and scans. It does not run `make lint` or the e2e
tests, so run those yourself when the change reaches the cluster.

## Rules that keep it safe

- **Every argument is an enum or a bounded integer** in the tool's schema, built with `schemaFor`.
  Never add a free-form string, path, or anything that names a pod, namespace, file or command.
- **Every change in `pkg/lab` calls `RequireLab` first.** The handler does not do it for you. It is
  what limits changes to the kind lab, and only when the operator allows them.
- **Change tools use `changeTool` and `changeHandler`**, which give one change at a time and an audit
  line. Read tools use `readTool` and `readHandler`.
- **Cluster text is hostile.** Return it through `Fence`, cap it, and clean it with `Clean`. Report API
  failures with `apiFailure`, which keeps the address and identity out of what the caller sees.
- **No shell, no `kubectl`, no listener.** Use `client-go`. An HTTP transport needs real
  authentication first.
- **Add only the RBAC a call uses**, in `lab/rbac.yaml`, in the narrowest namespace. Never Secrets.
- **Only claim what a test proves.** `docs/security.md` lists a control only if a test enforces it.
- **A scenario must leave the lab as it found it:** `Reset` in `pkg/lab/lab.go` must undo whatever it
  changes, as that is what runs on failure, panic and SIGTERM alike.

Never run the `generic` janitor provider on kind: kind nodes share the host's kernel, so it could
reboot the host. Never apply `lab/rbac.yaml` where a real GPU Operator runs.

## How to add things

**A tool**

1. Add any cluster logic to `pkg/lab`, with a test. A change calls `RequireLab` first.
2. Register it in `registerReadTools` or `registerChangeTools` in `pkg/server/server.go`, with a name,
   a title, a description, and a schema from `schemaFor` (or `withEmptyInput`).
3. Add it to `TestToolsAreListedAndMarkedReadOrChange`. Send hostile values through
   `TestBadArgumentsAreRefusedAndNeverReachTheCluster` if it takes arguments.
4. Add a row to the README's tool table, then run `make docs`.
5. Add any verbs it needs to `lab/rbac.yaml`, then run `make lab-fixture lab-token` and `make e2e`.
6. If it adds a control or a risk, update `docs/security.md`.

**A stage:** add a constant and an entry in `stageChecks` in `pkg/lab/stages.go`, with a test that a
failed query never counts as reached. Then `make docs`, as its name is in the schema.

**A scenario:** add it to `scenarios` in `pkg/lab/scenarios.go`, built from `waitStep` and fixed steps.
Make `Reset` undo what it changes. Add a test to `tests/e2e/`, then `make docs` and `make e2e`, and
commit the result in `docs/test-results/`.

**An XID:** add it to `AllowedXIDs` only after it has run on the lab. **A log source:** add it to
`LogComponents`, with RBAC if it is in a new namespace. Then `make docs`.

**A setting:** add an `Env...` constant and read it in `pkg/lab/kubeconfig.go`. Add it to the README's
settings table, `--help` in `main.go`, and `server.json`, which a test checks.

**A pipeline:** CI files are vendored from [omerfsen/cicd](https://github.com/omerfsen/cicd), and
`install.sh` overwrites local edits. Change them there and re-vendor. Settings go in
`.github/cicd/config`.

## Commits, PRs and releases

- Conventional Commits with a short one-line subject, normally no body. No `Signed-off-by` here: that
  is for the upstream PR only. No AI attribution anywhere.
- One branch and one PR per change. Squash-merge once every check passes:
  `gh pr merge --squash --body ""`.
- `feat`, `fix`, `perf`, `security`, `revert` and `deps` cut a release; `docs`, `ci`, `test`, `chore` and
  `refactor` do not. The release App opens and merges its own version PR, then tags it. The tag builds
  the image and the binaries.
- After a release, record it in a `docs:` PR: the image digest in the README and `docs/security.md`,
  the version in `server.json`, and what was verified in `docs/build-log.md`.
- Add what failed and why to `docs/build-log.md`, dead ends included. Say what was not tested.
- Never write a host name or internal domain in this repo or on GitHub: say "build machine".
