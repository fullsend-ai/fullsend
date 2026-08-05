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
output), and the sibling agentic-ci harness demonstrated the failure mode this
gap invites: content capture enabled unconditionally by default. A single
env var must never be able to route agent conversations to an arbitrary
backend.

## Decision

Level 3 activates only when three independently-owned conditions all hold.
While any of them is absent or off, runs stay metadata-only with no error.
A run that affirmatively requests capture — conditions 1 and 2 both on —
but cannot satisfy the rest of the contract fails before sandbox creation
rather than degrading silently:

1. **Operator opt-in (env):** `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`
   set to `true` (or the equivalent `span_only`); `false` or unset is off,
   and any other value is a hard error regardless of the other conditions —
   ambiguous intent is never read as off. Delivered to runs as CI-native
   infra plumbing per [ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md).
2. **Content-owner consent (harness):** `telemetry.content_capture: true` in
   the agent's harness. Consent is deliberately per-agent — content
   sensitivity varies by agent and repository, and the harness file's
   CODEOWNERS review is the consent mechanism — which is why this knob lives
   on the harness rather than `config.yaml` under
   [ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md)'s placement rule.
3. **Allowlisted destination:** the resolved OTLP traces endpoint host must
   appear in the `FULLSEND_CONTENT_CAPTURE_ALLOWED_ENDPOINTS` org variable,
   following the [ADR 0082](0082-workflow-host-allow-list.md) allow-list
   shape (default-minimal, CLI-managed, surfaced in status). With no
   allow-list no capture request can succeed, so implementation merges
   inert; every host addition links a recorded governance sign-off.

A capture request also fails closed when the secret redactor is disabled
(`security.enabled: false` or `host_scanners.secret_redactor: false`), when
the SDK is disabled, or when the selected runtime has no content adapter.

Content flows over OTLP export only. `run-telemetry.jsonl` keeps its
documented metadata-only contract, enforced by an attribute allow-list at the
file exporter. Captured content is assembled post-iteration from the
transcript and harness inputs into OTel GenAI semantic-convention aggregated
attributes (v1.37.0 shape, pinned and stamped as a span attribute) on the
existing per-iteration `agent` spans; it passes through secret redaction at
assembly, with hits recorded as security findings (an invariant check in
[ADR 0021](0021-jsonl-reasoning-trace-exposure.md)'s sense), and the export
path strips any content attribute lacking the redaction marker.
Reasoning/thinking text is not captured — a deliberate narrowing of
ADR 0050's Level 3 sketch, which anticipated reasoning for LLM-judge
scorers: reasoning traces carry the sensitivity this repo treats most
conservatively, and extending scope to them requires a new ADR. The capture
adapter is runtime-scoped behind the runtime interface (Claude first);
sandbox-side self-enablement is closed by
[ADR 0085](0085-sandbox-environment-variable-denylist.md).

## Consequences

- Content cannot flow by accident: no single variable, file, or workflow
  edit enables capture, and every implementation PR merges inert.
- The non-production restriction on content is mechanically enforced — the
  allow-list, not prose, decides where content may go, and lifting it is one
  reviewable org-variable change with a recorded sign-off.
- Redaction is structurally unavoidable: unmarked content attributes are
  stripped at export even when all gates pass.
- The exporter stays backend-agnostic (generic GenAI attributes, no
  backend-specific coupling), and metadata-only Levels 1–2 are unchanged.
- Legitimate enablement costs three configuration steps across two owners —
  deliberate friction, documented in the operator guide.
