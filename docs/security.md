# Security

This server lets an LLM read a Kubernetes cluster and break a lab on purpose, so the
main question is how little it can do when something goes wrong. A control is listed
here only if the code or a test enforces it. Tests named `Test...` are in `pkg/`; the
ones marked (e2e) also ran against a real kind cluster.

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
| A supply-chain attacker | Dependencies, bases, GitHub Actions, the registry |

There is no network attacker: the server speaks stdio and opens no port.

## Controls

### Reaching the wrong cluster

| Control | Proved by |
|---|---|
| Changes are off unless the operator sets exactly `NVSENTINEL_MCP_ALLOW_CHANGES=true` | `TestChangesAreOffUnlessAllowed`, `TestOnlyTheExactValueAllowsChanges`, `TestChangesAreOffByDefault` (e2e) |
| Changes need the lab's context, which must be a `kind-` context | `TestChangesAreRefusedOnAnotherContext`, `TestTheLabContextMustBeAKindContext`, `TestAnotherContextIsRefused` (e2e) |
| Changes need every node's provider ID to start with `kind://` | `TestChangesAreRefusedOnANodeThatIsNotKind` |
| A fault goes only into a pod whose every container, init and ephemeral included, comes from the simulated image's exact repository | `TestFaultsAreRefusedOnAnyOtherImage` |
| One kubeconfig, set by the operator. `KUBECONFIG` and `~/.kube/config` are never read, and no tool argument can choose a file | `TestOpenUsesOnlyTheConfiguredFile` |
| The kubeconfig is read once per call and held in memory for that call. Editing it takes effect on the next call, so it must stay private | `TestAChangedCurrentContextCannotRedirectAnOpenLab` |
| A kubeconfig others can read is refused. **Not on Windows**, which has no such modes: keep it where only you can read it | `TestOpenRefusesABadKubeconfig`, `TestTheModeCheckAppliesOutsideWindows` |
| A kubeconfig with an `exec` or `auth-provider` plugin is refused before any API call, as it would run a command | `TestOpenRefusesABadKubeconfig` |
| So is one that points at credential files, whose modes are unchecked, or that impersonates another user | `TestOpenRefusesABadKubeconfig` |
| So is one that turns off TLS verification or does not use https, as the token could reach another server | `TestOpenRefusesAnUnsafeCluster` |
| `HTTP(S)_PROXY` is ignored, so credentials cannot be sent to a proxy set in the environment | `TestTheEnvironmentProxyIsIgnored` |

Reading is not limited to the lab, on purpose: later runs read a real NVKE cluster.
There the limit must come from the API server: use a token with read-only RBAC.

### Least privilege in the cluster

`lab/rbac.yaml` gives the server its own ServiceAccount, with only the verbs its calls use.
It can list nodes, events and the pods it reports on; read logs in `nvsentinel`; delete pods
and exec only in `gpu-operator`; and patch only the `nvidia-dcgm` Service. It cannot read Secrets. `lab/mint-kubeconfig.sh` makes a
kubeconfig with an 8-hour token and no admin certificate, keeps the token out of argv,
and checks the result: no certificate, the expected identity, no access to Secrets. All
the e2e tests ran as this identity.

**Never apply `lab/rbac.yaml` where a real GPU Operator runs.** It allows exec and delete
on any pod in `gpu-operator`. That is fine on the lab and wrong anywhere else.

### Hostile arguments

- Every argument is an enum or a bounded integer in the tool's schema, and unknown fields
  are refused, before any tool code runs. `TestBadArgumentsAreRefusedAndNeverReachTheCluster`
  sends hostile values through a real MCP session, asserts the schema refused them, and
  asserts the cluster saw no call.
- The injected command is fixed. Only the XID varies, from an allowlist of tested values. It
  runs once: exec uses websocket only, so no error can fall back to a second protocol and run it again.
- There is no shell and no `kubectl`: the server calls the API with `client-go`.

### Hostile output

- Logs and events are wrapped in a marker with a random value per call, so their text
  cannot close it (`TestATextCannotCloseItsFence`), and the server's instructions tell the
  client it is data. This helps the model but cannot guarantee it ignores what it reads.
  The real limit is what the tools can do: a fooled model still only has these nine tools.
- Control characters and terminal escapes are removed. Output is capped at 64 KiB, log
  lines at 2000, event messages at 512 characters, errors at 500. What a pod prints during an
  injection is capped and never returned.
- A failed cluster call is reported by its kind only, such as "not permitted". client-go's
  text holds the API server's address and the caller's identity, so it goes to the server's
  log (`TestAnAPIFailureHidesTheAddressAndIdentity`).
- A crash or a panic returns only `error executing tool <name>`, with details kept in the
  server's log (`TestACrashShowsNoDetail`, `TestAPanicShowsNoDetail`). A missing kubeconfig's
  error does not name its path.

### Keeping the lab sane

- One change at a time: a second is refused, not queued, so a reset cannot race another
  fault (`TestOnlyOneChangeRunsAtATime`). Waits are capped at 900 seconds, with at most 4
  running (`TestWaitsAreLimited`). Tool calls that pass schema validation are limited to 10 per second, with a burst of 20, as the
  MCP spec requires (`TestToolCallsAreRateLimited`). Malformed calls are refused before that and never reach the cluster.
- A failed query never counts as a stage reached (`TestAFailedQueryNeverCountsAsReached`).
  A healthy node reaches no fault stage (`TestAHealthyNodeReachesNoFaultStage`).
- A scenario resets the lab if a step fails, panics, or its context ends because the client
  left or a signal came (`TestAFailedStepResetsTheLab`, `TestAPanicInAStepResetsTheLab`,
  `TestAStoppedContextResetsTheLab`). SIGTERM cancels a running scenario, which resets the lab
  before the server exits (`TestShutdownCancelsARunningChange`, `TestShutdownWaitsForARunningChange`,
  `TestSIGTERMResetsTheLab` (e2e), which reads the audit line: `cancelled`, `restored: ok`). A second
  signal ends the process at once. **SIGKILL, a second signal or a power cut can leave a fault
  behind:** `reset_lab` undoes it.
- Every attempted change writes one JSON line to stderr: tool, arguments, outcome, duration
  (`TestEveryChangeIsAudited`, `TestARefusedChangeIsAudited`).

### The image

- One static binary on distroless static, about 32 MB: no shell, package manager or libc.
- It never runs as root: the image sets user 65532 (`TestImageDoesNotRunAsRoot` (e2e)). The command
  below starts it as your own user instead, so it can read your private kubeconfig.
- Bases pinned by digest, Go dependencies by `go.sum`, with `GOTOOLCHAIN=local` so a `go.mod`
  change cannot download a toolchain.
- No credentials in the image. `.dockerignore` lets in only what the build needs.
- Scanned with Trivy and OSV: no fixable CRITICAL or HIGH. The cicd `docker` pipeline repeats
  the Trivy scan before pushing, then signs the digest with cosign and attaches an SBOM and
  build provenance. v0.5.0's provenance was verified with `gh attestation verify`; its signature
  and SBOM are attached, but were not checked by hand.

Run it with everything off, and pin the digest:

```shell
docker run --rm -i --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --user "$(id -u):$(id -g)" --memory=128m --cpus=1 --pids-limit=64 --network=kind \
  --volume "$HOME/.kube/nvsentinel-lab-mcp-docker.yaml:/kube/config:ro" \
  docker.io/omerfsen/nvidia-nvsentinel-mcp@sha256:8c3a2a9f284812704f1e23a450d077e638f720602f44b8e490c6835bada36dff
```

`--network=kind` is not a strict limit: that network is shared by every kind cluster on the host
and can reach the host's gateway. It still beats `--network=host`, so do not use that, and mount
that one file, never `~/.kube`. Add `--env NVSENTINEL_MCP_ALLOW_CHANGES=true` only for a run that should
change the lab. The e2e tests ran the image with exactly these flags.

A release's binaries come from the same tag, with their own provenance, `checksums.txt` and SBOM as
release assets. Verify what you pull, with the digest of the release you use:

```shell
gh attestation verify oci://docker.io/omerfsen/nvidia-nvsentinel-mcp@sha256:<digest> \
  --repo omerfsen/nvidia-nvsentinel-mcp
cosign verify docker.io/omerfsen/nvidia-nvsentinel-mcp@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/omerfsen/nvidia-nvsentinel-mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Left out on purpose

- **Auth or TLS on the transport:** stdio has no listener. If this ever gets an HTTP
  transport, it needs real authentication first.
- **An ingress:** nothing here listens.
- **Scanning logs for secrets:** patterns give false confidence. The server's identity cannot
  read Secrets, and logs are capped.
- **Asking for human confirmation in the server:** an agent client may answer it itself. The
  real check is the client's permission prompt, so change tools are marked destructive.

## Known limits

- Tool annotations are hints: only a client that honours them prompts a human.
- Fencing lowers the risk from prompt injection but does not remove it.
- The image is amd64 only.
- The binaries for anything but Linux on x86-64 are built and not run. They are not code-signed
  or notarized: check them with `checksums.txt` and `gh attestation verify`.
- `make lint` and the e2e tests do not run in CI. `go vet` and `go test -race` do.
- Allowing a tag as well as a digest for the simulated image is deliberate, so the server
  works on NVSentinel's own demo. It checks which image this is, not its exact bytes.
