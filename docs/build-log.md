# Build log

What was built, in order, with each failure, dead end and fix. Newest at the bottom.
The host: 92 GiB RAM, 24 threads, one RTX 4080 SUPER, shared with other lab work.

## Step 1: Read the sources (2026-11-04)

NVSentinel v1.30.0 is the newest (GitHub, ghcr and the docs default agree). The README
and overview at that tag still show v1.29.0 in their example; it is stale.

- **Direct node commands on KVM:** yes for reboot. The `generic` provider runs a privileged
  Job doing `chroot /host reboot` in the guest. `docs/janitor.md`'s table shows "—", but the
  code and the configuration docs show it works. It cannot terminate a node.
- **The `generic` provider must never run on kind:** kind nodes are privileged containers on
  the host kernel, so its sysrq option could reboot the real host.
- **Syslog XID detection on kind is out:** the check filters on `_TRANSPORT=kernel`, so
  `logger` is ignored, and `/dev/kmsg` from a kind node writes to the host's log.
- **NVKE does not install NVSentinel**, in our collection or upstream; only AICR's recipe does.
  NVKE's repair path needs CAPNICo, which cannot run on a site with no DPUs.

## Step 2: What this repo is (2026-11-04)

It began as an Ansible collection, `omerfsen.nvsentinel`. That changed: installs go through
omerfsen.nvke and omerfsen.aicr, and this repo holds the MCP server and example test results.
It was renamed to `nvidia-nvsentinel-mcp`. No ingress: nothing listens, and the chart has no UI.

AICR pins NVSentinel v1.28.0 and cert-manager v1.20.2 and has no newer release. Its `--data`
override moves them with no fork, in https://github.com/omerfsen/aicr/pull/8. That install ran
on kind here: nvsentinel v1.30.0, cert-manager v1.21.3, one-member Percona.

## Step 3: A first version in Python (2026-11-04)

**Dead end.** The first server was Python, shelling out to `kubectl`. It proved the design:
`xid95` passed on kind. It also showed the cost: 160 MB, and official `kubectl` builds carry
fixable HIGH CVEs (`golang.org/x/net`, `x/text`), so it needed a custom `kubectl` build. Then the
aim became a PR to NVSentinel, which has 33 Go modules and 4 Python projects, so it was rewritten.
The Python was removed from the repo. Its one lesson that matters here:

- **`dcgm_outage` first failed.** Patching the Service does not break an established connection,
  so the monitor never noticed. An independent review predicted this. **Fix:** also restart the
  hostengine.

## Step 4: The Go server (2026-11-04)

`client-go` instead of `kubectl`, and the official MCP SDK v1.8.0. `kubectl` left the image, with
its CVEs. The image went from 160 MB to 32.5 MB.

**Failure:** two rewrites of this step's shell command were stopped by a safety check on `rm`. The
cause was a careless `rm -rf *` in a temp folder, then a script with `rm` inside a heredoc. **Fix:**
the temp folder is new and needs no removal, and `lab/mint-kubeconfig.sh` empties a bad file
instead of removing it.

**Failure:** `go get golang.org/x/net@v0.60.0 golang.org/x/text@v0.41.0` conflicted, as x/net needs
a newer x/text. OSV then found more: x/net v0.60.0 itself, and `go.opentelemetry.io/otel`. **Fix:**
x/net v0.62.0 and x/text v0.43.0. `make image` and OSV are now clean.

**Failure:** a test of a stopped scenario failed: the fake client ignores a cancelled context, so the
scenario ran to the end. It showed a real gap: a step that ignores its context would carry on. **Fix:**
the scenario checks its context before every step.

## Step 5: Security review (2026-11-04)

An independent reviewer read the code twice. Each finding was checked against the code. Fixed:

- A killed scenario left the fault in the lab. It now resets on any failure, cancellation, panic or
  SIGTERM, and `reset_lab` exists for what SIGKILL leaves.
- `lab/mint-kubeconfig.sh` never proved the admin certificate was gone, and put the token in argv.
- Unbounded waits, interleaving changes, uncapped output, and a kubeconfig path in status.
- Claims in the security doc that its tests did not prove.

What was left out and why is in [security.md](security.md).

A third review, of the Go code, found more:

- **SIGTERM did not reset the lab, and I had claimed it did.** The SDK waits for running handlers on
  stdio and does not cancel them, so a scenario ran on to its normal end. The e2e test passed because
  that end also leaves the lab healthy: it could not tell the two apart. **Fix:** the server cancels
  handlers itself on shutdown, a second signal ends the process, and the test now reads the audit line.
  It took 126 s before, as the whole scenario ran, and takes 66 s now.
- **Exec could inject twice.** It fell back to SPDY after any error, including a failed command.
  **First fix:** only after a failed upgrade, with output capped. Injection then failed, as websocket exec
  is a `GET` and needs `get` on `pods/exec`, not just `create`. The error was hidden by another fix from
  the same review, so it had to be read from the server's log. **Final fix:** websocket only, with no
  fallback. Kubernetes 1.36 needs both `get` and `create` for it: a run with `get` alone was refused too.
  Why the first fix did not fall back to SPDY was not established, as that code is gone.
- Cluster text and client-go errors reached the caller unfenced, and unknown fields passed on tools
  with no arguments. Insecure kubeconfigs were accepted. RBAC had unused verbs.

## Step 6: Results (2026-11-04)

On kind, NVSentinel v1.30.0 installed by omerfsen.aicr, as the server's own ServiceAccount:

- The real binary over stdio: all 7 e2e tests that apply pass, including SIGTERM mid-scenario.
- The image with every restriction in security.md: all 7 that apply pass. The SIGTERM test is
  skipped, as a signal goes to the docker client, and the non-root check runs only here.
- Reports: [test-results](test-results/README.md).

## Step 7: Installing it (2026-11-04)

The request was compatibility with skills.sh and `gh mcp add`. **`gh` v2.103.2 has no `mcp` command**,
only `gh skill`, so that part cannot exist yet. `gh skill` and skills.sh both find
`skills/<name>/SKILL.md`, so one skill serves both, next to `go install`, `claude mcp add` and a
`server.json` for the official MCP Registry. A skill holds instructions, not a server, so an install
is still two steps.

NICo's `nico-mcp` was the nearest thing to a house style. See [upstream.md](upstream.md).

## Step 8: First release (2026-11-04)

The default App name, `omerfsen-nvidia-nvsentinel-mcp-release`, is 38 characters, and GitHub allows
34: the App is `omerfsen-nvsentinel-mcp-release`. With cicd v0.8.0's `docker` pipeline and
release-please on, the App opened and merged its release PR, tagged v0.1.0, and the tag pushed the image.

`docker.io/omerfsen/nvidia-nvsentinel-mcp:0.1.0`,
`sha256:bdbbbd1892b93fe142b1ef74363b79759c03460aaffad0e3ffe46d7e3f751944`: reports version 0.1.0, runs as 65532, passes every e2e test that applies to an
image on kind, and Trivy is clean. `gh attestation verify` confirms it was built by `docker.yml` at
`refs/tags/v0.1.0`. Three Sigstore bundles are attached to it (signature, SBOM, provenance).

## Not run yet

A real GPU, AICR on NVKE, remediation, and checking the image's cosign signature and SBOM by hand.
