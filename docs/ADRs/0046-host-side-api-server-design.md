---
title: "46. Host-side API server design for sandboxed agents"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - api-server
  - credential-isolation
---

# 46. Host-side API server design for sandboxed agents

Date: 2026-06-02

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

[ADR 0024](0024-harness-definitions.md) introduced the `api_servers` harness field as planned but not
implemented. [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)/[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md) established the host-side REST server as credential delivery tier 3
of the credential delivery model — for cases where providers (credential delivery tier 2) cannot
handle, originally scoped to credentials in request bodies and response
transformation. Practice revealed additional cases beyond provider reach:
long-running operations exceeding MCP timeouts, operations the sandbox
deliberately blocks (container builds, see
[NVIDIA/OpenShell#113](https://github.com/NVIDIA/OpenShell/issues/113)),
and multi-step atomic operations.

The `host-side-api-server` experiment
([fullsend-ai/experiments#28](https://github.com/fullsend-ai/experiments/pull/28))
validated the end-to-end flow with two servers (Go container builder, Python
repo provisioner), testing lifecycle management, API discoverability, L7 policy
tuning, per-run auth, and file transfer. This ADR records design decisions
informed by that experiment and by subsequent review discussion on the
implementation model.

## Options

### API discoverability

Three approaches were tested. `/tools.json` (structured tool-use schema) was
the most token-efficient under full access (92k tokens vs 107k for OpenAPI,
100k for baked instructions). Both discovery-based methods fail under
restricted policies where the endpoint is blocked; baked instructions succeed
(84k) but can drift from the actual API. OpenAPI's verbose structure adds
context tokens without proportional benefit for LLM agents.

### Per-run authentication

**UUID bearer token via provider placeholders:** simple, proven, no key
management. The proxy resolves the placeholder — the real token never enters
the sandbox. No claims or expiry.

**Short-lived JWTs with claims:** per-operation authorization and audit trail,
but adds signing key management and JWT library dependencies in every server.
The L7 policy already restricts reachable endpoints, making JWT claims a
redundant second layer.

### File transfer between server and sandbox

**`openshell sandbox upload/download` from the server:** works today, validated
by experiment, handles real-time exchange during request handling. Couples
server to OpenShell CLI.

**Shared host mount:** transparent POSIX access, no CLI coupling. Depends on
OpenShell mount support that is not yet universally available
([NVIDIA/OpenShell#1509](https://github.com/NVIDIA/OpenShell/issues/1509)).

**HTTP multipart via the API:** fully portable, but large files through the L7
proxy add overhead.

### Implementation model

**Language-agnostic process contract:** each server is an independent binary
in any language. The runner manages it via CLI flags (`--port`, `--token`,
`--bind-address`) and expects `/healthz`, `/tools.json`, bearer auth, and
clean `SIGTERM` shutdown. The experiment validated this with Go and Python
servers. Maximally flexible, but every server re-implements boilerplate (CLI
parsing, health endpoints, auth middleware, security hardening).

**Go interface:** fullsend-maintained servers implement an internal Go
interface, compiled to a single binary. Simplifies deployment (one binary,
no runtime dependencies) and enables stub-based testing. The process
contract remains the runner's enforcement boundary — the interface is an
internal pattern, not a runner requirement. Provides a seam for adapting
deployment topology across workflow platforms (sidecars, pods).

## Decision

Adopt the host-side API server design with the following implementation model,
policy model, and security requirements. The
[`host-side-api-server` experiment](https://github.com/fullsend-ai/experiments/pull/28)
validated the end-to-end flow; the implementation model was refined during
review.

**Process contract.** Every compiled API server binary accepts `--port`,
`--token`, and `--bind-address` flags; serves `GET /healthz`
(unauthenticated) and `GET /tools.json` (structured tool-use schema) for agent
discovery; validates bearer tokens on all other endpoints; and shuts down
cleanly on `SIGTERM`. Servers write logs to stderr; the runner collects and
bundles logs from all API servers so they are available for inspection after
the run completes. The runner starts servers after pre-script, health-checks
before sandbox creation, and tears down after sandbox destruction. If a server
crashes mid-run, the run fails.

**Implementation model.** The runner enforces the process contract above — any
binary that satisfies it is a valid API server. Internally, fullsend-maintained
servers use a Go interface as the recommended pattern: each server provides
HTTP handlers and a `/tools.json` schema, and the `main()` wires the
implementation to the process contract (CLI flags, health endpoints, auth
middleware, signal handling). Harnesses reference a compiled Go binary, the
workflow runner downloads it to the host, and `fullsend run` manages its
lifecycle. The interface makes servers testable via stub implementations and
provides the seam for adapting deployment topology across workflow platforms
(e.g., sidecars or separate pods on Kubernetes/OpenShift). Implementors who
need more control can write their own `main()` against the process contract
directly.

**Network policy via composable provider profiles.** Each API server ships
atomic capability profiles — one per logical group of endpoints (e.g.,
`builder-build`, `builder-push`, `builder-read`). Harnesses list which profiles
to attach. Composition is additive per OpenShell's provider-backed policy
composition
([NVIDIA/OpenShell#1037](https://github.com/NVIDIA/OpenShell/pull/1037)).
Different agent roles compose different capability sets for the same server.
Requires OpenShell >= v0.0.37 and
[#776](https://github.com/fullsend-ai/fullsend/issues/776).

**Per-run auth:** UUID bearer token via OpenShell provider placeholders. JWTs
are a future enhancement when per-operation claims become necessary.

**File transfer:** `openshell sandbox upload/download` from the server during
request handling. Shared host mount
([NVIDIA/OpenShell#1509](https://github.com/NVIDIA/OpenShell/issues/1509))
will be evaluated as an alternative when available.

**Bind address:** servers default to `127.0.0.1`, runner overrides to
`0.0.0.0` for sandboxed agents.
[NVIDIA/OpenShell#1633](https://github.com/NVIDIA/OpenShell/issues/1633)
(supervisor-proxied host-local endpoints) would eliminate this requirement.

> **Note (2026-07):** OpenShell PR
> [#1560](https://github.com/NVIDIA/OpenShell/pull/1560) removed the
> `allowed_ips` requirement for explicitly declared endpoints. The `0.0.0.0`
> bind is still needed (rootless Podman cannot bind to the bridge gateway IP),
> but `allowed_ips` templating is no longer part of the setup. Validated in
> [fullsend-ai/experiments#42](https://github.com/fullsend-ai/experiments/pull/42).
> [#1633](https://github.com/NVIDIA/OpenShell/issues/1633) remains relevant
> for eliminating the `0.0.0.0` exposure itself.

**Security hardening:** timing-safe token comparison, 1 MB request body limits,
rate limiting on unauthenticated endpoints, credential scrubbing in error
messages, bounded in-memory state.

## Consequences

- The `api_servers` harness field ([ADR 0024](0024-harness-definitions.md)) will gain a `providers` sub-field and
  defined runtime behavior. The initial implementation targets Go servers
  behind an internal interface; the process contract keeps the door open for
  other languages.
- Implementing this design requires
  [#776](https://github.com/fullsend-ai/fullsend/issues/776) (provider-backed
  policy composition) as a prerequisite.
- Servers are coupled to the OpenShell CLI for file transfer until shared host
  mounts are universally available.
- Servers must bind to `0.0.0.0` on shared hosts, widening the attack surface
  until [NVIDIA/OpenShell#1633](https://github.com/NVIDIA/OpenShell/issues/1633)
  ships.
  > **Note (2026-07):** `allowed_ips` templating is no longer required
  > (OpenShell PR [#1560](https://github.com/NVIDIA/OpenShell/pull/1560)).
  > The `0.0.0.0` bind and its attack surface remain until
  > [#1633](https://github.com/NVIDIA/OpenShell/issues/1633) ships.
- API servers (credential delivery tier 3) are now clearly scoped to cases providers cannot
  handle: long-running operations, sandbox capability gaps, credentials in
  request bodies, response transformation, and multi-step atomic operations.
- Fullsend-maintained servers follow a Go interface pattern (testable,
  platform-portable). The process contract remains the enforcement boundary
  — servers in other languages or with custom `main()` implementations are
  valid as long as they satisfy it.
