# ADR-XXX: `Tooling` — An MCP Server for Testing NVSentinel

Draft for a proposal to NVSentinel, on their template. A maintainer owns this decision.

## Context

NVSentinel's fault pipeline can be exercised by hand with `demos/local-fault-injection-demo`, or by
the e2e suite. AI coding agents are welcome in this project (`CONTRIBUTING.md`, `AGENTS.md`), but an
agent checking a change has to drive a cluster through a general `kubectl`, which can do anything its
credentials allow, including on a cluster it did not create.

The options were: leave it to `kubectl` and rules in `AGENTS.md`; add more scripts to the demo; or add
a Model Context Protocol (MCP) server that offers only the operations a test needs.

## Decision

Add a standalone Go MCP server, for test clusters, that reads NVSentinel's state and injects faults
into the simulated hostengine only. It is not installed by the chart and is not part of the pipeline.

## Implementation

- A new module, `mcp-server/`, using `client-go` and the official Go MCP SDK, over stdio only.
- Reading tools: node and pod state, a wait for a pipeline stage, logs and events.
- Change tools: inject an XID, clear it, reset the lab, and run a scenario end to end. They are off
  unless the operator allows them, need a `kind-` context and `kind://` nodes, and inject only into a
  pod running only `ghcr.io/nvidia/nvsentinel-fake-dcgm`.
- A ServiceAccount with only what the tools need; no access to Secrets.
- No change to modules, the chart, the protobufs or the event store.

## Rationale

- An agent gets a small set of checked operations instead of a general `kubectl`, so a mistake or a
  prompt injection from a log line has little it can reach.
- It reads effects from the cluster, like the demo's scripts, and a failed query never counts as success.
- A scenario resets the lab on failure, cancellation or SIGTERM, so a test does not leave a fault behind.

## Consequences

### Positive
- Agents and people can run the pipeline's checks the same way, with a structured report.
- It sits outside the fault path, so it cannot affect production behaviour.

### Negative
- A new module and two new dependencies to maintain: the MCP SDK and `jsonschema-go`.
- Its stages read node conditions and labels, so changes to those need a change here.

### Mitigations
- Those conditions and labels are already treated as public API under the backwards compatibility rules.
- Its tests run with `client-go`'s fake client, with an optional e2e run on kind.

## Alternatives Considered

### `kubectl` and rules in `AGENTS.md`
**Rejected** because: rules do not limit what a command can do, and a prompt-injected agent ignores them.

### More scripts in the demo
**Rejected** because: scripts give an agent no schema or structured result, and they still need a shell.

### An MCP server over HTTP
**Rejected** because: it would need authentication and a port, and stdio needs neither.

## Notes

- Non-goals: production use, remediation, and any real GPU's hostengine.
- Tested on kind with NVSentinel v1.30.0 and the simulated hostengine. Not tested on real GPUs.
