# Build log

What was built, in order, with each failure, dead end and fix. Newest at the bottom.
The host: 92 GiB RAM, 24 threads, one RTX 4080 SUPER, shared with other lab work.

## Step 1: Read the sources (2026-11-04)

NVSentinel v1.30.0 is the newest (GitHub, ghcr and the docs default agree). The
README and overview at that tag still show v1.29.0 in their example; it is stale.

- **Direct node commands on KVM:** yes for reboot. The `generic` provider runs a
  privileged Job doing `chroot /host reboot` in the guest. `docs/janitor.md`'s table
  shows "—", but the code and the configuration docs show it works. It cannot
  terminate a node.
- **The `generic` provider must never run on kind:** kind nodes are privileged
  containers on the host kernel, so its sysrq option could reboot the real host.
- **Syslog XID detection on kind is out:** the check filters on `_TRANSPORT=kernel`,
  so `logger` is ignored, and `/dev/kmsg` from a kind node writes to the host's log.
- **NVKE does not install NVSentinel**, in our collection or upstream; only AICR's
  recipe does. NVKE's repair path needs CAPNICo, which cannot run on a site with no DPUs.

## Step 2: What this repo is (2026-11-04)

It began as an Ansible collection, `omerfsen.nvsentinel`. That changed: installs go
through omerfsen.nvke and omerfsen.aicr, and this repo holds the MCP server and its
tests. The repo was renamed to `nvidia-nvsentinel-mcp`. No ingress: nothing listens,
and the chart has no UI.

AICR pins NVSentinel v1.28.0 and cert-manager v1.20.2 and has no newer release.
AICR's `--data` override moves them with no fork: rendered offline, see that repo's
build log (https://github.com/omerfsen/aicr/pull/8).

## Step 3: The server (2026-11-04)

**Failure:** SDK 2.3.0 has no `mcp.server.fastmcp`; it was renamed to `MCPServer`.

**Failure:** two tests failed because every refusal reached the client as `Error
executing tool ...`. The SDK treats any exception but `ToolError` as a crash and
hides its text. **Fix:** catch `LabError` in the server and raise `ToolError`, which
keeps `lab.py` free of MCP.

## Step 4: Security review (2026-11-04)

An independent reviewer's findings, checked against the code and fixed: unbounded
waits, change tools that could interleave, uncapped output, the kubeconfig path in
status, a guard that skipped init containers, and kubectl found through `PATH`. Also
added: changes off by default, refusing credential plugins, pinning the context,
a minimal environment for kubectl, and an exact image digest in the guard. What was
left out and why is in [security.md](security.md).

## Step 5: The image (2026-11-04)

**Dead end:** distroless Python on Debian 12 (Python 3.11.2). Trivy found 47 fixable
HIGH findings (libexpat, Kerberos and others), so the pipeline's gate would refuse it.
Distroless on Debian 13 (Python 3.13.5, 60 MB): none. Chainguard Python: also none,
but 71 MB, so Debian 13 it is.

**Dead end:** official kubectl v1.36.6 and v1.37.2 both carry fixable HIGH findings in
`golang.org/x/net` and `x/text`. An ignore file would have hidden them. **Fix:** build
from `k8s.io/kubectl` v0.36.6 with those updated. x/net v0.60.0 needs x/text v0.42.0,
so a first attempt at v0.41.0 failed.

**Result:** 160 MB (kubectl 59 MB, Python dependencies 40 MB). Trivy shows no fixable
CRITICAL or HIGH. As user 65532, with a read-only filesystem, no capabilities and no
network, it answers MCP `initialize`.

## Step 6: CI (2026-11-04)

cicd's `docker` pipeline could not push to Docker Hub.
https://github.com/omerfsen/cicd/pull/23 adds that, with the token only in the push
environment, plus an SBOM and build provenance on each digest.

## Not run yet

A live install elsewhere on the host holds its memory, so nothing has been deployed:
no kind cluster, no `lab/` script, no `make e2e`, no RBAC on a cluster, no token
kubeconfig, and no image pushed. Tests so far use a fake kubectl and a real MCP session.
