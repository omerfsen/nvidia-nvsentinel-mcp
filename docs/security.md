# Security

This server lets an LLM read a Kubernetes cluster and break a lab on purpose, so
the main question is how little it can do when something goes wrong. A control is
listed here only if the code or a test enforces it.

## Threat model

| Asset | Worry |
|---|---|
| Other clusters, VMs and kubeconfigs on the host, and a real GPU | A tool reaches them |
| The lab | A fault is left injected, or two changes interleave |
| The LLM's context | Pod logs and events carry instructions, and the LLM follows them |
| The public image, and anyone who pulls it | A tampered dependency, base image or build |

| Attacker | Path |
|---|---|
| A prompt-injected LLM | Hostile tool arguments, or tools called in a hostile order |
| Whoever controls what the cluster logs | Text returned by `component_logs` and `node_events` |
| A supply-chain attacker | Dependencies, bases, kubectl, GitHub Actions, the registry |

There is no network attacker: the server speaks stdio and opens no port.

## Controls

**Reaching the wrong cluster**

| Control | Where | Proved by |
|---|---|---|
| Changes are off unless `NVSENTINEL_MCP_ALLOW_CHANGES=true` | `Lab.require_lab` | `test_changes_are_off_unless_allowed` |
| Changes need the context `kind-nvsentinel-lab` and every node's `kind://` provider ID | `Lab.require_lab` | `test_changes_are_refused_off_the_lab` |
| The context is pinned with `--context` once the guard passes | `Lab.run` | `test_the_context_is_pinned_after_the_guard` |
| A fault goes only into a pod whose every container, init and ephemeral included, runs the simulated image by digest | `Lab.fake_dcgm_pod` | `test_changes_are_refused_off_the_lab` |
| One kubeconfig, set by the operator; no tool argument can choose it. `~/.kube/config` is never read | `Lab.__init__`, `Lab.run` | `test_kubectl_gets_a_minimal_environment` |
| A kubeconfig others can read is refused | `Lab.check_kubeconfig` | `test_a_kubeconfig_others_can_read_is_refused` |
| A kubeconfig with an `exec` or `auth-provider` plugin is refused before any API call, as it would run a command | `Lab.check_credentials` | `test_reading_refuses_a_credential_plugin_before_any_api_call` |

Reading is not limited to the lab, on purpose: later runs read a real NVKE cluster.
There the limit must come from the API server: use a token with read-only RBAC.

**Least privilege in the cluster**

`lab/rbac.yaml` gives the server its own ServiceAccount. It can read nodes, events
and the pods it reports on; delete pods and exec only in `gpu-operator`; and patch
only the `nvidia-dcgm` Service. It cannot read Secrets. `lab/mint-kubeconfig.sh`
makes a kubeconfig with an 8-hour token and no admin certificate, so even a bug in
these guards is limited by the API server. Not yet run on a cluster.

**Hostile arguments**

- Every argument is an enum or a bounded integer in the tool's schema, checked
  before any tool code runs; `test_bad_arguments_are_refused_and_never_reach_kubectl`
  sends hostile values through a real MCP session and asserts kubectl never ran.
- kubectl runs from an argument list, never a shell, with a minimal environment: no
  inherited `KUBECONFIG`, proxy settings or cloud credentials.
- The XID allowlist has only values that were tested.

**Hostile output**

- Logs and events are wrapped in a marker with a random value per call, so their
  text cannot close it, and the server's instructions tell the client it is data.
  This helps the model but cannot guarantee it ignores what it reads. The real limit
  is what the tools can do: a fooled model still only has these eight tools.
- Control characters and terminal escapes are removed. Output is capped at 64 KiB,
  log lines at 2000, event messages at 512 characters, kubectl errors at 500.
- A crash returns only `Error executing tool <name>`
  (`test_a_crash_shows_no_detail`). Status does not show the kubeconfig's path.

**Keeping the lab sane**

- One change at a time: a second is refused, not queued, so a scenario's restore
  cannot race another fault. Waits are capped at 900 seconds, with at most 4 running.
- A failed query never counts as a stage reached
  (`test_a_failed_query_never_counts_as_reached`). A failed scenario restores the lab
  (`test_a_failed_scenario_stops_and_restores`).
- Every attempted change writes one JSON line to stderr: tool, arguments, outcome
  and duration (`test_every_change_is_audited_on_stderr`).

**The image**

- Distroless Python on Debian 13: no shell or package manager, user 65532.
- Bases pinned by digest, Python dependencies by hash (`--require-hashes`, wheels
  only), Go dependencies by `go.sum`.
- `kubectl` is built from `k8s.io/kubectl` with fixed `golang.org/x/net` and
  `x/text`, because official v1.36.6 and v1.37.2 builds have fixable HIGH findings.
  It also leaves out the cloud auth plugins.
- No credentials in the image. `.dockerignore` lets in only what the build needs.
- The cicd `docker` pipeline scans before pushing: an image with a fixable CRITICAL
  or HIGH is never pushed. It then signs the digest with cosign and attaches an SBOM
  and build provenance.

Run it with everything off, and pin the digest:

```shell
docker run --rm -i --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,noexec,nosuid,size=32m \
  --memory=256m --cpus=1 --pids-limit=64 --network=kind \
  --volume "$HOME/.kube/nvsentinel-lab-mcp-docker.yaml:/kube/config:ro" \
  docker.io/omerfsen/nvidia-nvsentinel-mcp@sha256:<digest>
```

`--network=kind` reaches only the lab. Do not use `--network=host`, and mount that
one file, never `~/.kube`. Add `--env NVSENTINEL_MCP_ALLOW_CHANGES=true` only for a
run that should change the lab.

Verify what you pull:

```shell
cosign verify docker.io/omerfsen/nvidia-nvsentinel-mcp@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/omerfsen/nvidia-nvsentinel-mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Left out on purpose

- **Auth or TLS on the transport:** stdio has no listener. If this ever gets an HTTP
  transport, it needs real authentication first.
- **An ingress:** nothing here listens.
- **Rate limits:** there is one lab and one change at a time.
- **Scanning logs for secrets:** patterns give false confidence. The server's
  identity cannot read Secrets, and logs are capped.
- **`ctx.elicit` as a human check:** an agent client may answer it itself. The real
  check is the client's permission prompt, so change tools are marked destructive.

## Known limits

- Tool annotations are hints: only a client that honours them prompts a human.
- Fencing lowers the risk from prompt injection but does not remove it.
- The image is amd64 only.
- `make test` does not run in CI yet; cicd has no Python pipeline.
- Nothing here has run against a cluster yet. See [the build log](build-log.md).
