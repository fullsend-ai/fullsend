---
title: "84. Level 3 content capture activation contract"
status: Accepted
relates_to:
  - operational-observability
  - security-threat-model
topics:
  - observability
  - telemetry
  - security
---

# 84. Level 3 content capture activation contract

Date: 2026-08-05

## Status

Accepted

## Context

[ADR 0050](0050-distributed-tracing-instrumentation.md) defines Level 3 —
prompt/completion content in spans — as an explicit opt-in, but leaves the
activation semantics unspecified: which configuration enables it, what happens
on partial configuration, and where content may flow. Content is the
highest-sensitivity telemetry fullsend emits (proprietary source, PII, tool
output), and a comparable harness ships the failure mode this gap invites:
[agentic-ci's harness](https://github.com/opendatahub-io/agentic-ci/blob/main/src/agentic_ci/harness.py)
sets the runtime's content-logging variables unconditionally for every run.
A single env var must never be able to route agent conversations to an
arbitrary backend.

## Decision

Level 3 activates only when three independently-owned conditions all hold.
While any of them is absent or off, runs stay metadata-only with no error.
A run that affirmatively requests capture — conditions 1 and 2 both on —
but cannot satisfy the rest of the contract fails before sandbox creation
rather than degrading silently:

1. **Operator opt-in (env):** `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`
   set to `true` or `span_only` (equivalent; the upstream conventions mandate
   opt-in but leave values to implementations, so the accepted set is defined
   here). `false` or unset is off; any other value is a hard error regardless
   of the other conditions — ambiguous intent is never read as off. Delivered
   as CI-native infra plumbing per [ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md).
2. **Content-owner consent (harness):** `telemetry.content_capture: true` in
   the agent's harness — content sensitivity is per-agent, and the harness
   file's CODEOWNERS review is the consent mechanism. This is a surface
   [ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md)'s two placements
   (org-wide `config.yaml` fields, `{AGENT}_` env vars) do not cover;
   neither carries the per-agent review semantics this consent requires.
3. **Allowlisted destination:** the resolved OTLP traces endpoint's host must
   appear in the comma-separated `FULLSEND_CONTENT_CAPTURE_ALLOWED_ENDPOINTS`
   org variable — `host` or `host:port`, matched exactly and
   case-insensitively; no wildcards (an unreviewed subdomain must never
   qualify), no scheme or path matching. Unset or empty means no capture
   request can succeed, so implementation merges inert; each addition records
   its governance sign-off reference. The org variable keeps this gate's
   owner distinct from the harness's CODEOWNERS — personal-account
   repositories have no second owner and cannot enable Level 3. Operational
   shape (default-minimal, CLI-managed, surfaced in status) follows
   [ADR 0082](0082-workflow-host-allow-list.md); the matching rules are
   defined here, not there.

A capture request also fails closed when the secret redactor is disabled
(`security.enabled: false` or `host_scanners.secret_redactor: false`), when
the SDK is disabled, or when the selected runtime has no content adapter.

**The agent runtime's native content telemetry is never enabled.** Level 3
re-exports the transcript fullsend already extracts through fullsend's own
pipeline — one exporter, one redaction path, no second trace identity;
[ADR 0085](0085-sandbox-environment-variable-denylist.md) makes the
in-sandbox alternative unconstructible.

Content flows over OTLP export only; `run-telemetry.jsonl` keeps its
metadata-only contract via an attribute allow-list at the file exporter.
Content is assembled post-iteration from the transcript and harness inputs
into OTel GenAI aggregated attributes (v1.37.0 shape, pinned and stamped as
a span attribute) on the existing per-iteration `agent` spans, under
per-kind size budgets with structure-preserving truncation (validating
backend attribute limits precedes the pilot; tracked in the implementation
issue). Redaction runs at assembly: a hit masks the value and records a
security finding, encoding evasion drops the affected part whole, and export
strips any content attribute lacking the redaction marker.
Reasoning/thinking text is not captured — a deliberate narrowing of
ADR 0050's sketch (reasoning is the trace class this repo treats most
conservatively); extending scope requires a new ADR. The capture adapter is
runtime-scoped behind the runtime interface (Claude first).

## Consequences

- Content cannot flow by accident: no single variable, file, or workflow
  edit enables capture, and every implementation PR merges inert.
- Content cannot flow to an arbitrary destination — the allow-list, not
  prose, decides where content may go, and every entry carries a recorded
  sign-off reference.
- Redaction is structurally unavoidable: unmarked content attributes are
  stripped at export even when all gates pass.
- The exporter stays backend-agnostic (generic GenAI attributes, no
  backend-specific coupling), and metadata-only Levels 1–2 are unchanged.
- Legitimate enablement costs three configuration steps across two owners —
  deliberate friction, documented in the operator guide.
