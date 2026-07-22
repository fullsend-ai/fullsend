---
title: "71. Harness snapshot and forge pointer correlation for agent traces"
status: Proposed
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

Proposed

## Context

[ADR 0050](0050-distributed-tracing-instrumentation.md) defines how fullsend
generates traces (OTel Go SDK; Level 1 = `run-telemetry.jsonl`; Level 2 =
OTLP). [Operational observability](../problems/operational-observability.md)
requires forge **pointers** (repo + SHA, change id, pipeline run) rather than
duplicating diffs, and a stable answer to: “what harness/config produced this
run?”

After the OTel SDK migration, `run-summary.json` was removed; the sole Level 1
telemetry file is `run-telemetry.jsonl`. Eval wrappers that `export` ambient CI
variables are the wrong layer: harness identity is a **run-time** concern of
`fullsend run`, and downstream tools must not scrape CI env as the source of
truth.

Cross-project join field semantics (Provenance / MLflow tag names) live in the
shared join contract maintained with the certification report schema; this ADR
decides only what **fullsend** writes.

## Decision

### 1. Harness snapshot artifact (Level 1)

Every `fullsend run` writes **`harness-snapshot.json`** into the run output
directory (next to `run-telemetry.jsonl`) at run start, after the root span
exists.

Contents are pointers and a config fingerprint only (no diffs, prompts, or
skill bodies), including at least:

- Harness identity: agent, role, slug, model, skills, harness path
- `harness_content_sha` (content hash of the resolved harness file)
- Forge/CI pointers when known: `forge_platform`, `repository_url`,
  `ref_revision`, `ref_name`, `change_id`, `pipeline_run_id`,
  `pipeline_run_url`
- Trace join: `trace_id`, `traceparent`

Forge/CI fields are filled by fullsend when writing the snapshot (`FULLSEND_*`
overrides first, then standard CI env). Downstream consumers **read the JSON
file** (and/or later stores that ingest it), not ambient CI env.

`OTEL_SDK_DISABLED=true` suppresses span export (`run-telemetry.jsonl` / OTLP)
but **does not** suppress `harness-snapshot.json` — it is the run-start config
contract, not span export.

### 2. Root span attributes (Level 1 + Level 2)

The same join keys are set on the root `run` span (`vcs.*`, `cicd.*`,
harness content SHA, forge platform) so OTLP backends receive them without
parsing the JSON. Local JSON remains the forensic / handoff contract.

### 3. What we do not do

- Do **not** treat eval scripts’ CI env exports as the harness-snapshot path.
- Do **not** embed PR diffs or prompt content in the snapshot.
- Do **not** redefine cross-project report field ownership in this ADR; link
  the shared join contract from Related.

## Consequences

**Easier:** One decided artifact per run; operators and eval loggers can join
runs to forge/CI and to `run-telemetry.jsonl` via `trace_id`.

**Harder:** Dispatched child runs must inherit forge context via env so the
child snapshot is complete; GitLab/Bitbucket coverage depends on CI vars or
`FULLSEND_*` overrides. Implementation is a follow-up issue after this ADR is
accepted.

## Related

- [ADR 0050](0050-distributed-tracing-instrumentation.md)
- [ADR 0005](0005-forge-abstraction-layer.md)
- Shared join contract: [provenance_forge_pointers.md](https://github.com/RHEcosystemAppEng/ABEvalFlow/blob/main/Docs/provenance_forge_pointers.md)
  (land on ABEvalFlow `main` via the join-contract doc PR)
- [#2368](https://github.com/fullsend-ai/fullsend/issues/2368)
- [#294](https://github.com/fullsend-ai/fullsend/issues/294)
