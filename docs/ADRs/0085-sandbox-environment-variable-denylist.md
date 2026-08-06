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
`host_files` copied into the workspace, including env files the sandbox
shell sources from `.env.d/` — the platform's supported delivery mechanism
for runtime configuration such as inference-provider selection. The agent
runtime (Claude Code) also reads its own telemetry and control variables
from that environment, including variables that enable native content
capture and redirect its export to an arbitrary endpoint. Today a harness
can set those with no fullsend gate involved: `reservedSandboxKeys` denies
specific infrastructure keys but nothing telemetry-related, and a copied
file can overwrite the sourced `.env` itself. This is the same surface the
OIDC credential denylist (#5837) already guards for mint variables, and the
failure mode is the one
[ADR 0084](0084-level-3-content-capture-activation-contract.md)'s gates
exist to prevent — reached from inside the sandbox instead of around it.
`TRACEPARENT` injection additionally corrupts trace identity
([ADR 0050](0050-distributed-tracing-instrumentation.md)).

## Decision

Deny the variables that carry content-capture or export-routing risk, on
both injection surfaces, while leaving the platform's env delivery mechanism
— including legitimate runtime configuration like `CLAUDE_CODE_USE_VERTEX`
and metrics-only telemetry to an operator's own collector — untouched:

- **Denied set:** the content-capture flags, enumerated exactly
  (`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`,
  `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT`,
  `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA`); the export-routing family
  (`OTEL_EXPORTER_*`); and trace identity (`TRACEPARENT`, `TRACESTATE`).
  The set is a single named list in code so future additions are one-line
  changes.
- **Keys:** denied among `env.sandbox` keys, including forge-merged
  harness bases.
- **Files:** `host_files` may not overwrite fullsend-owned files
  (`/sandbox/workspace/.env`, `.claude/settings.json`); any file landing in
  a sourced location (`.env.d/*`, shell rc/profile files) has its resolved
  content scanned, and assignments of denied variables fail the run.
  Mounting env files into `.env.d/` remains supported — the scan gates what
  they may set, not whether they may exist.

Violations of the denied set are pre-flight hard errors before sandbox
creation, consistent with the fail-closed posture of the
[security threat model](../problems/security-threat-model.md); they are not
lint diagnostics, which do not abort a run. This is stricter than the
existing mechanism's warn-and-skip and applies only to the denied set; the
existing reserved keys keep their current behavior.

## Consequences

- Closes a live gap: a harness can no longer enable the runtime's native
  content telemetry or redirect its export with no fullsend gate involved,
  and ADR 0084's contract cannot be bypassed via harness-controlled env.
- Existing harnesses keep working: provider selection and metrics-only
  telemetry via `.env.d/` are outside the denied set; only the rare harness
  that sets a denied variable fails loudly and must move that setting to a
  fullsend-owned surface — a breaking change carried in release notes.
- Guarantees are conditional on a platform-trusted sandbox image: a custom
  `image:` can bake env below this pre-flight's sight, so the denylist's
  value is collapsing the attack surface to a few loud, reviewable
  artifacts (image reference, denied-set diffs). Image trust (reference
  allow-listing, digest pinning) is a separate control.
- The denylist extends the #5837 surface rather than introducing a second
  enforcement path.
