# Eval Measurements

Eval measurements score agent runs from their **OpenTelemetry traces** for
trends over time. They are **not** functional evals (PR-gate fixtures under
`fullsend-ai/agents` `eval/<agent>/`).

**Decided:** [ADR 0087](../../ADRs/0087-eval-measurements-online-trace-scoring.md).
Telemetry baseline: [Distributed Tracing](./distributed-tracing.md)
([ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md)).

## Prerequisites

- A repository with fullsend installed and producing `run-telemetry.jsonl`
  (see [Distributed Tracing](./distributed-tracing.md)).
- A measurement manifest for the agent (stock agents get one from
  `fullsend-ai/agents@v0`; custom agents need a local YAML under
  `${FULLSEND_DIR}/eval/measurements/`).

## Architecture (read this first)

Eval measurements are the concept of scoring traces.
[OTEL primary facts](../../glossary.md#otel-primary-facts) are what happened
on the run (`run-telemetry.jsonl`).
[OTEL derived products](../../glossary.md#otel-derived-products) are scores
computed from that trace (`eval-measurements.jsonl`). The step is
[fail-open](../../glossary.md#fail-open): it never blocks delivery.

Fullsend does not pick an observability product for scores. The portable
contract is a local JSONL artifact next to telemetry; remote export reuses
the same OpenTelemetry (`OTEL_EXPORTER_OTLP_*`) configuration as agent
traces.

OTLP (OpenTelemetry Protocol) is the wire format that carries spans and
scores to any compatible backend — Phoenix, MLflow, Jaeger, etc.

```text
fullsend run
  └─ always writes  output/<runDir>/run-telemetry.jsonl
  └─ if OTEL_EXPORTER_OTLP_* set → live OTLP export of agent spans
       (any compatible backend — ADR 0050)

fullsend eval-measure   (same GHA job, fail-open, after run)
  └─ writes  output/<runDir>/eval-measurements.jsonl when at least one
       new score is produced (+ eval-measure-ledger.txt for idempotency)
  └─ if OTEL_EXPORTER_OTLP_* set → OTLP export of scores as
       gen_ai.evaluation.result span events on the same TraceID
       (the W3C Trace ID shared with the agent run — fail-open;
       local JSONL always wins)
```

| Artifact | When | Purpose |
|---|---|---|
| `run-telemetry.jsonl` | Every run | OTLP JSON TracesData lines (local source of truth for spans) |
| `eval-measurements.jsonl` | Every measured run | One JSON object per score (`name`, `label`, `value`, `explanation`, `trace_id`, …). On `label: skip`, `value` is unused (serialized as `0`; ignore it). |
| Remote agent spans | OTEL configured | Same spans the local file holds |
| Remote scores | OTEL configured | Child span `fullsend.eval_measure` + **span event** `gen_ai.evaluation.result` ([normative GenAI events](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-events.md#event-gen_aievaluationresult); [library support matrix](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/reference/reports/gen-ai-evaluation-result-event.md)) correlated by TraceID / parent span ID. Attribute names follow the convention; the convention’s carrier is a **log record** — fullsend uses a span event because only a traces OTLP exporter is configured (log-side consumers will not auto-discover these; a logs-path emit is follow-up). |

Orgs choose Phoenix, MLflow, Jaeger, or another collector independently.
Any OTLP backend can **correlate** scores to the agent run by TraceID.
Vendor score UIs (for example MLflow Assessments panels) may still need a
collector or side consumer that maps the evaluation event — fullsend does
not call those product APIs. Scores are not rewritten into
`run-telemetry.jsonl` (derived products must not mutate primary facts).

## Measurements vs functional evals

| | Functional evals | Eval measurements |
|---|---|---|
| **Repo path** | `agents/eval/<agent>/` | `agents/eval/measurements/<agent>.yaml` |
| **When** | PR / CI fixture gates | After each managed agent run |
| **Input** | Case fixtures + judge harness | `run-telemetry.jsonl` only |
| **Blocks delivery?** | Yes (when wired as a check) | Never (fail-open) |

## Ownership

| Concern | Repo |
|---|---|
| Parser, scorer **implementations**, CLI, GHA post-step | `fullsend-ai/fullsend` |
| Default manifests for stock agents (which scorers / ids) | `fullsend-ai/agents` |
| Overrides, opt-out, BYOA agent manifests | Consumer repo (`FULLSEND_DIR`) |

Defaults for stock agents live next to those agents in `fullsend-ai/agents`.
Managed jobs fetch them from `agents@v0` when no local file exists — users do
**not** duplicate YAML into every install. Put a file under
`${FULLSEND_DIR}/eval/measurements/` only to change policy or score a custom
agent.

**Activation is two-step:** merge manifests (e.g.
[fullsend-ai/agents#722](https://github.com/fullsend-ai/agents/pull/722))
**and** cut a `v0.x.y` release that re-points the floating `v0` tag
([#6384](https://github.com/fullsend-ai/fullsend/issues/6384)). Until that
release lands, managed GHA/GitLab `eval-measure` steps stay provisional
(missing remote manifest → clean skip). Local `FULLSEND_DIR` overrides work
today and are covered by CLI tests.

Scorer *code* stays in fullsend: the measure CLI is the released engine that
understands `run-telemetry.jsonl`. Agents ships **policy** (manifests), not Go.
EM-001 (`trace_fitness`) evaluates fullsend’s telemetry contract across agents;
stock agents opt in via manifests here / in `agents`.

| Change | Where |
|---|---|
| New Go scorer or new declarative `assert:` primitive | `fullsend` PR |
| New measurement `id` / enable / thresholds for a stock agent (existing scorer) | `agents` PR |
| Org-specific policy for stock or custom agents | Local override in the consumer repo |

**Planned (not in first ship):** declarative checks in the manifest (attribute
exists, ratio/threshold bands) so most agent-specific policy is YAML-only.
Until then, agent-specific math still lands as a named Go scorer in fullsend,
enabled only for the agents that list it.

First scorer: **`trace_fitness`** (catalog id `em-001`) — span tree + expected
attributes so later scorers can trust the trace.

Manifest shape (first ship — enablement only):

```yaml
agent: review
measurements:
  - id: em-001
    scorer: trace_fitness
    version: 1
```

Illustrative **logic-as-config** (future declarative engine — not wired yet).
Attribute names in the example match attrs fullsend emits on the **`run`**
span today; they are **not** a contract for the declarative surface:

```yaml
agent: code
measurements:
  - id: em-001
    scorer: trace_fitness
    version: 1
  - id: em-010
    scorer: declarative
    version: 1
    where:
      span: run
    checks:
      - name: turn_token_ratio
        assert: ratio_lte
        numerator: gen_ai.usage.output_tokens
        denominator: fullsend.num_turns
        max: 8000
```

### Versioning

Not a platform “v1.” Each entry versions its own contract:

- **`id`** — stable catalog id (`em-001`). New concept → new id.
- **`scorer`** — which Go scorer to run (`trace_fitness`).
- **`version`** — bump when pass/fail semantics change; scores store
  `em-001@1`. Ledger is idempotent per `(trace_id, name, id@version)`.

The EM-001 `exit` check only requires that `exit_code` is **present** on the
run span (instrumentation fitness). It does **not** treat `exit_code == 0` as
success. After [#5944](https://github.com/fullsend-ai/fullsend/pull/5944),
run/agent **OTLP Status** (and `fullsend.transcript_error`) are the
success/failure signal for outcome scorers.

Pre-script **skipped** runs set `fullsend.prescript.skipped=true` on the root
span and never create a sandbox. EM-001 records `label: skip` for those traces
instead of failing the span-tree / model / usage checks. Runs with no `agent`
span (never reached an iteration — e.g. sandbox/provider failure) and runs
where agent spans flushed but the root `run` span never ended (hard kill /
timeout) are also `label: skip`, so pass/(pass+fail) measures the telemetry
contract rather than runner health. An unknown `scorer:` string (for example a
newer `agents@v0` manifest this binary does not implement yet) also writes
`label: skip`, not `fail`. Trend pass-rate as `pass / (pass + fail)` and drop
`skip`.

## Adjacent telemetry work

| Topic | Relationship to measurements |
|---|---|
| Level 3 content capture ([ADR 0050](../../ADRs/0050-distributed-tracing-instrumentation.md); activation draft closed without merge in [#5947](https://github.com/fullsend-ai/fullsend/pull/5947)) | First ship scores Level 1/2 metadata fitness. **Planned:** content-aware scorers on Level 3 prompt/completion bodies once L3 is implemented — that is the real quality signal. Measure CLI is host-side after the sandbox exits. |
| [#5944](https://github.com/fullsend-ai/fullsend/pull/5944) Span status from run outcome *(merged)* | Unblocks outcome scorers keyed on Status, not raw exit alone. |
| Semantic observability / observer / lessons (draft closed without merge in [#2423](https://github.com/fullsend-ai/fullsend/pull/2423)) | Observer + lessons → fixtures remains a sibling idea; measurements are the online score path. |
| [#5524](https://github.com/fullsend-ai/fullsend/pull/5524) Harness snapshot / forge join keys *(open)* | Complementary join/identity proposal beside telemetry; measurements are derived scores, not primary run facts. |

## Same-job timing

```text
GitHub Actions job
├── fullsend run
├── fullsend eval-measure   # reads output/<runDir>/run-telemetry.jsonl; never fails the job
└── upload-artifact         # includes both JSONL files under output/

GitLab CI agent job
├── fullsend run            # --output-dir $CI_PROJECT_DIR/output
├── fullsend eval-measure   # always (even if run failed); || true
└── artifacts: output/      # when: always (parity with GHA upload of output/)
```

Add `output/` to the consuming repo's `.gitignore` so local GitLab-checkout
runs do not stage telemetry accidentally. The GitLab per-repo scaffold embeds
a recommended `.gitignore` fragment (asserted in tests) but does **not**
install it as a root file — that would overwrite an existing consumer ignore
list. When `--output-dir` sits inside `--target-repo` (GitLab layout),
`fullsend run` omits that top-level directory from the sandbox tarball and
`.git/info/exclude`; sibling layouts (GitHub Actions) are unchanged.

## Manifest resolution in CI

`fullsend eval-measure` resolves the measurement manifest for the agent:

1. Explicit `--registry <path>` when the managed job materializes a **trusted**
   local override (GitLab: `git show ${DEFAULT_BRANCH_SHA}:.fullsend/eval/measurements/…`;
   GHA: `git show` of `pull_request.base.sha` or `GITHUB_SHA`), else
2. SHA-pinned `eval/measurements/${AGENT}.yaml` from public `fullsend-ai/agents`
   (same `v0` → commit SHA, allowlist, hash, and fetch audit as harness
   fallback — not a floating `raw.githubusercontent.com/.../v0/...` curl).
   GitHub Actions injects `GH_TOKEN` for that `GetRef`. GitLab CI has no
   GitHub token by default; because `agents` is public, `GetRef` still runs
   unauthenticated (~60 req/hr per IP). On busy shared runners, export
   `GH_TOKEN` / `GITHUB_TOKEN` to avoid rate-limit skips.

Managed GHA/GitLab scaffolds deliberately **do not** pass `--fullsend-dir` into
`eval-measure`: that flag would prefer `${FULLSEND_DIR}/eval/measurements/`
from the checked-out MR/PR working tree and let an author change which
already-shipped scorers run (or their `id@version`) for that job's trend —
unlike kill-switch/role config, which already reads the default/base tip.
Local/dev invocations may still pass `--fullsend-dir` when you intentionally
want the working-tree override.

Step 2 is how stock-agent defaults reach every install. Step 1 is org override
on the **default/base branch** only (or an explicit `--registry` path).

Platform telemetry is `run-telemetry.jsonl` at the top of the host run
directory (`agent-<name>-<pid>-<unix>` under the CI output base). Nested
`iteration-N/output/run-telemetry.jsonl` copies and leftover sibling runDirs
are ignored.

Missing manifest or telemetry → log and exit `0` (skip). `--registry` and
`--telemetry` remain for local/debug use.

## CLI

```bash
fullsend eval-measure \
  --agent review \
  --fullsend-dir "${FULLSEND_DIR}" \
  --output-dir path/to/output
```

- `--agent` + `--output-dir` is the managed-job form. `--registry` /
  `--telemetry` remain for pointing at explicit files.
- `--offline` rejects the remote `agents@v0` fetch (local FULLSEND_DIR
  manifest only), matching `fullsend run --offline`.
- Exit `0` when a score is `fail` — scores are data.
- Exit `0` when telemetry or the manifest is missing (skip).

## Implementation note

Today the measure CLI writes local `eval-measurements.jsonl` whenever at
least one new measurement row is appended (including `label: skip`). No
file is written when telemetry/manifest is missing, no traces match, or
every candidate row is already in the ledger.

When `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`
is set, newly written scores also export as OTLP **span events**
(`fullsend.eval_measure` + `gen_ai.evaluation.result`) on the same
`trace_id`. Attribute names follow the GenAI convention; the convention’s
carrier is a log record — fullsend uses the traces path because only a
traces exporter is configured (see artifact table). Export is fail-open and
does not rewrite `run-telemetry.jsonl`.
The idempotency ledger keys local rows; a remote OTLP failure after a
successful local write will not retry that row on the next run (remote is
best-effort once). Re-export offline by clearing the ledger or pointing at
a fresh out dir.

Managed measure assumes one platform `run-telemetry.jsonl` per runDir (each
`fullsend run` creates a unique `output/fs-<slug>-<hash>/`). If inbound
`TRACEPARENT` is present and unsampled, score export skips only rows whose
`trace_id` matches that parent TraceID (same orphan-avoidance rule as agent
`parentSampledProcessor`); other TraceIDs in the batch still export.
Cross-run cost/correlation rollup is out of scope here (see hierarchical
work-graph IDs).
