---
title: "85. Sandbox denylist for telemetry and runtime control variables"
status: Accepted
relates_to:
  - security-threat-model
topics:
  - security
  - sandbox
  - telemetry
---

# 85. Sandbox denylist for telemetry and runtime control variables

Date: 2026-08-05

## Status

Accepted

## Context

A harness controls part of the sandbox environment: `env.sandbox` keys and
`host_files` copied into the workspace, including files the sandbox shell
sources. The agent runtime (Claude Code) reads its own telemetry and control
variables from that environment — including variables that enable native
content capture and export to an arbitrary endpoint. Today a harness can set
these with no fullsend gate involved: `reservedSandboxKeys` denies specific
infrastructure keys but no telemetry prefixes, and `host_files` destinations
are unconstrained, so a copied file can overwrite the sourced `.env` or land
new files in `.env.d/`. This is the same surface the OIDC credential
denylist (#5837) already guards for mint variables, and the failure mode is
the one [ADR 0084](0084-level-3-content-capture-activation-contract.md)'s
gates exist to prevent — reached from inside the sandbox instead of around
it. `TRACEPARENT` injection additionally corrupts trace identity
([ADR 0050](0050-distributed-tracing-instrumentation.md)).

## Decision

Extend the accepted sandbox env-denylist mechanism to runtime telemetry and
trace-control variables, on both injection surfaces:

- **Keys:** deny `OTEL_*`, `CLAUDE_CODE_*`, and `TRACEPARENT` among
  `env.sandbox` keys, including forge-merged harness bases.
- **Destinations:** deny `host_files` destinations that the sandbox sources
  or the runtime consumes (`/sandbox/workspace/.env`, `.env.d/*`, shell
  rc/profile files, `.claude/settings.json`) — including forge-specific
  `host_files` — and scan the resolved content of any file landing in a
  remaining sourced location for denied assignments.

For these prefixes and destinations, violations are pre-flight hard errors
before sandbox creation, consistent with the fail-closed posture of the
[security threat model](../problems/security-threat-model.md); they are not
lint diagnostics, which do not abort a run. This is stricter than the
existing mechanism on both axes — prefix matching instead of exact keys,
hard error instead of warn-and-skip — and applies only to the variables this
ADR adds; the existing reserved keys keep their current behavior.

## Consequences

- Closes a live gap: a harness can no longer enable the runtime's native
  content telemetry or redirect its export with no fullsend gate involved.
- ADR 0084's activation contract cannot be bypassed from inside the sandbox.
- Harnesses that legitimately set a denied key today fail loudly at
  pre-flight and must migrate to the supported configuration surfaces — a
  breaking behavior change carried in release notes.
- The denylist extends the #5837 surface rather than introducing a second
  enforcement path, though with stricter semantics (prefixes, hard error)
  scoped to the new variables; future telemetry-prefix additions reuse it.
