---
title: "71. Harness snapshot and forge pointer correlation for agent traces"
status: Accepted
relates_to:
  - operational-observability
topics:
  - observability
  - telemetry
  - opentelemetry
  - forge
---

# 71. Harness snapshot and forge pointer correlation for agent traces

Date: 2026-07-22

## Status

Accepted

## Context

[ADR 0050](0050-distributed-tracing-instrumentation.md) defines how fullsend
generates traces (OTel Go SDK; Level 1 = `run-telemetry.jsonl`; Level 2 =
OTLP). [Operational observability](../problems/operational-observability.md)
requires forge **pointers** (repo + SHA, change id, pipeline run) rather than
duplicating diffs, and a stable answer to: “what harness/config produced this
run?” Forge-neutral hosting concepts remain those in
[ADR 0005](0005-forge-abstraction-layer.md); this ADR only decides how join
pointers are recorded on a run.

After the OTel SDK migration, `run-summary.json` was removed; the sole Level 1
telemetry file today is `run-telemetry.jsonl`. Eval wrappers that `export`
ambient CI variables are the wrong layer: harness identity is a **run-time**
concern of `fullsend run`, and downstream tools must not scrape CI env as the
source of truth. Cross-project join field names live in a shared join contract;
this ADR decides only what **fullsend** writes.

## Options

**A. Root-span attributes only (no snapshot file).** Join keys exist in OTLP
backends, but offline/forensic consumers must parse spans, and keys disappear
when span export is disabled.

**B. Embed a snapshot event in `run-telemetry.jsonl`.** One Level 1 file, but
couples the harness/config contract to telemetry format and exporter semantics.

**C. Eval-script / ambient CI env exports.** Easy to prototype; unstable across
local runs, child dispatches, and forges; encourages scraping rather than an
explicit artifact.

**D. Dedicated `harness-snapshot.json` plus mirrored root-span attributes
(chosen).** Stable local run-start contract for offline consumers, plus
backend-friendly correlation for OTLP.

## Decision

Every `fullsend run` records run-start join keys by writing
**`harness-snapshot.json`** next to `run-telemetry.jsonl` **and**, when tracing
is enabled, setting the same keys on the root `run` span (`vcs.*`, `cicd.*`,
harness content SHA, forge platform). The file holds pointers and a config
fingerprint only (harness identity, content hash, forge/CI pointers when known)
— no diffs, prompts, or skill bodies. Forge/CI fields are filled at write time
(`FULLSEND_*` overrides first, then standard CI env). Consumers read the JSON
(or stores that ingest it), not ambient CI env.

When tracing is enabled, write the snapshot after the root span exists and
include `trace_id` / `traceparent` so the file joins to `run-telemetry.jsonl`
and OTLP. When `OTEL_SDK_DISABLED=true`, still write the snapshot (config/forge
contract, not span export) but **omit** `trace_id` / `traceparent` and skip
root-span attribute mirroring — there is no usable trace context.

## Consequences

- Operators and downstream loggers can join a run to forge/CI via a single
  decided artifact; when tracing is on, `trace_id` joins to `run-telemetry.jsonl`.
- OTLP backends receive join keys without parsing the JSON when tracing is on;
  local JSON remains the forensic / handoff contract.
- Dispatched child runs must inherit forge context via env so child snapshots
  are complete.
- GitLab/Bitbucket coverage depends on CI vars or `FULLSEND_*` overrides.
- Implementation is tracked separately ([#5449](https://github.com/fullsend-ai/fullsend/issues/5449));
  detailed field lists for cross-project consumers stay in the shared join
  contract, not duplicated here.

## Related

- [ADR 0050](0050-distributed-tracing-instrumentation.md)
- [ADR 0005](0005-forge-abstraction-layer.md)
- Shared join contract: [provenance_forge_pointers.md](https://github.com/RHEcosystemAppEng/ABEvalFlow/blob/main/Docs/provenance_forge_pointers.md)
- Implementation: [#5449](https://github.com/fullsend-ai/fullsend/issues/5449)
- [#2368](https://github.com/fullsend-ai/fullsend/issues/2368)
- [#294](https://github.com/fullsend-ai/fullsend/issues/294)
