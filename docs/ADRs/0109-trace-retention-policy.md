---
title: "109. Trace retention policy with administrator-tunable defaults"
status: Accepted
relates_to:
  - operational-observability
topics:
  - observability
  - telemetry
  - retention
---

# 109. Trace retention policy with administrator-tunable defaults

Date: 2026-09-08

## Status

Accepted

## Context

Fullsend agent runs produce two categories of trace artifacts:
JSONL reasoning transcripts stored with owner-scoped access
([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)), and OpenTelemetry
trace data at three telemetry levels
([ADR 0050](0050-distributed-tracing-instrumentation.md)) — local
`run-telemetry.jsonl` plus optional OTLP export. Eval
measurement scores are derived products of traces
([ADR 0087](0087-eval-measurements-online-trace-scoring.md)).

No decision exists on how long these artifacts are retained. Indefinite
retention maximizes audit and incident investigation capability but
increases storage cost and data sensitivity exposure — traces at Level 3
contain full prompt/completion pairs that may include proprietary source
code. Time-bounded retention limits exposure but may discard traces
needed for post-incident review. The
[observability problem doc](../problems/operational-observability.md)
lists this as an open question, and
[#6733](https://github.com/fullsend-ai/fullsend/issues/6733) (cold-storage
setup) depends on a retention policy decision.

Each adopting organization operates independently with its own
infrastructure ([architecture.md](../architecture.md)), so the platform
cannot impose a single retention period — retention must be a tunable
parameter for the administrator.

## Decision

Define default retention periods per artifact category. Administrators
override defaults through their OTLP backend configuration or storage
lifecycle rules — fullsend does not enforce retention itself.

**Default retention periods (guidance, not enforcement):**

| Artifact | Default | Rationale |
|---|---|---|
| Level 1/2 metadata traces (`run-telemetry.jsonl`¹, OTLP metadata spans) | 180 days | Low sensitivity (no content); sufficient for trend analysis and most incident investigations. |
| Level 3 content traces (OTLP spans and `run-telemetry.jsonl`¹ when content capture is enabled) | 30 days | High sensitivity (proprietary code, PII); short window limits exposure while covering active incident response. |
| JSONL reasoning transcripts ([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)) | 90 days | High content sensitivity (contains a superset of Level 3 content — full prompts, completions, tool calls, and reasoning); exposure risk reduced by owner-scoped access and credential scanning ([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)). Balances debugging, session resumption, and retro analysis needs against storage cost. |
| Eval measurement scores (`eval-measurements.jsonl`) | 365 days | Minimal sensitivity (numeric scores, no content); long retention supports trend analysis across model and prompt changes. |

¹ `run-telemetry.jsonl` is the sole local trace artifact ([ADR 0050](0050-distributed-tracing-instrumentation.md)). At Levels 1/2 it contains only metadata spans. When Level 3 content capture is enabled (`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`), the same file also contains full prompt/completion content. Administrators who enable Level 3 should apply the 30-day content trace retention to `run-telemetry.jsonl` instead of the 180-day metadata default.

**Tunability mechanism:** Fullsend documents these defaults but does not
implement retention enforcement. Retention is enforced at the storage
layer by the administrator:

- **GitHub Actions artifacts:** configured via the repository or
  organization artifact retention setting.
- **OTLP backends** (Jaeger, Tempo, MLflow, etc.): configured via
  the backend's native retention or TTL settings.
- **Cold storage** (GCS, S3, etc.): configured via bucket lifecycle
  rules.

This keeps retention enforcement in systems designed for it rather than
building a parallel lifecycle manager into the fullsend CLI.

**Regulatory and audit overrides:** Organizations with regulatory
requirements (SOC 2, HIPAA, financial audit) should extend metadata
trace retention to match their compliance window and may need to extend
or shorten content trace retention depending on whether audit or
data-minimization requirements dominate. The documentation notes this
trade-off without prescribing a specific compliance mapping.

## Consequences

- Adopters get documented defaults they can apply to their storage
  backends immediately, unblocking [#6733](https://github.com/fullsend-ai/fullsend/issues/6733)
  (cold-storage setup).
- No retention enforcement code is added to fullsend — retention remains
  a storage-layer concern, consistent with ADR 0050's principle that
  backend choice is an adopter decision.
- Level 3 content traces have the shortest default (30 days), reflecting
  their higher sensitivity; organizations that need longer content
  retention for debugging must accept the storage and exposure cost.
- Eval measurement scores have the longest default (365 days) because
  they contain no sensitive content and their value grows with
  historical depth.
- Future work may add a `retention:` field to `.fullsend/config.yaml`
  that the platform reads when managing its own artifact lifecycle, but
  this ADR does not require it.
