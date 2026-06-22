# Behavioral Thresholds for Functional Tests

Date: 2026-06-01

## Problem

Functional tests verify that agent pipelines produce correct side effects
(labels, comments, PR state), but they say nothing about *how* the agent got
there. An agent that applies the right label but burns 50 turns and $8 doing
it has a problem — one that quality judges can't catch.

When we build statistical evals (tracked in
[#73](https://github.com/fullsend-ai/fullsend/issues/73)), we'll observe
baseline distributions of turn count, token usage, and cost across many runs.
Those baselines should flow back into functional tests as thresholds: "this
test case should complete within N turns and $X." But we don't need to wait
for statistical evals to establish the discipline. We can require thresholds
now with rough baselines and refine them later.

## Design

### 1. `fullsend run` emits `metrics.json`

Claude Code's stream-json output includes a final event with execution
metrics. The fields we need are already present:

```json
{
  "total_cost_usd": 0.42,
  "num_turns": 8,
  "usage": {
    "input_tokens": 12000,
    "output_tokens": 3400,
    ...
  },
  "modelUsage": {
    "claude-opus-4-6": {
      "inputTokens": 12000,
      "outputTokens": 3400,
      "costUSD": 0.42,
      ...
    }
  }
}
```

**Implementation:** The `progressParser` in `internal/cli/progress.go`
already reads the stream-json NDJSON line by line. Extend `RunMetrics` to
capture `num_turns`, `total_cost_usd`, `input_tokens`, and `output_tokens`
from the final event. After all iterations complete, `fullsend run` writes
`metrics.json` to the run's output directory, aggregating across retries:

```json
{
  "num_turns": 12,
  "total_cost_usd": 0.58,
  "token_usage": {
    "input": 18000,
    "output": 5200
  },
  "iterations": 2,
  "tool_calls": 34
}
```

When retries occur, all values are summed. The functional test cares about
the total cost of getting the job done, not the cost of the successful
attempt alone.

### 2. `annotations.yaml` gets mandatory behavioral thresholds

Every test case must declare `max_turns` and `max_cost_usd`:

```yaml
# annotations.yaml
state: open
labels:
  required:
    - ready-to-code
    - bug

max_turns: 15
max_cost_usd: 2.00

triage_expectations: |
  ...
```

These are rough baselines today. When statistical evals provide observed
distributions, we tighten them. The values should be generous enough to
avoid flaky failures but tight enough to catch regressions (e.g., an agent
that loops).

### 3. Universal enforcement in `run-functional.sh`

The behavioral threshold checks are **not** per-skill judges in `eval.yaml`.
They are universal invariants enforced by the orchestrator so that:

- Every skill gets them automatically — no copying judge definitions.
- New skills can't opt out — the orchestrator enforces them before scoring.
- The harness judges remain focused on quality; the orchestrator handles cost.

The enforcement flow in `run-functional.sh`:

1. **Pre-flight validation:** Before running any case, verify that its
   `annotations.yaml` contains both `max_turns` and `max_cost_usd`. Fail
   fast if missing — this is a test authoring error, not a test failure.

2. **Post-run threshold check:** After the runner completes, compare
   `metrics.json` values against `annotations.yaml` thresholds. Log a clear
   pass/fail for each:
   ```
   Threshold: max_turns     15  actual  8   PASS
   Threshold: max_cost_usd  2.00  actual  0.42  PASS
   ```

3. **Threshold failures count toward the overall result.** A case that passes
   all quality judges but exceeds a behavioral threshold is a failure.

### 4. Why `max_turns` and `max_cost_usd` (not token counts)

We gate on two metrics, not four:

- **`max_turns`** — the most intuitive measure of agent efficiency. A turn
  is one assistant response. Excessive turns usually mean the agent is
  looping, retrying, or taking an indirect path. Easy to baseline by
  watching a few runs.

- **`max_cost_usd`** — captures token usage indirectly but accounts for
  model pricing differences. An agent that uses a cheaper model for
  sub-tasks costs less even at the same token count. Cost is what we
  actually care about controlling.

We do **not** gate on raw `input_tokens` or `output_tokens` because:

- Token counts vary with model context window, caching behavior, and prompt
  structure in ways that are hard to baseline without statistical data.
- Cost already captures tokens — gating on both is redundant.
- When statistical evals provide per-model token distributions, we can add
  token thresholds as a refinement. The `metrics.json` already records them.

### 5. ADR 0048 update

ADR 0048 gets a new section documenting this decision: behavioral thresholds
are mandatory for all functional test cases, enforced universally by the
orchestrator, and baselined roughly until statistical evals provide observed
distributions.

### 6. `fullsend-runner.sh` propagates `metrics.json`

The runner already captures `fixture-state.json`. It also needs to copy
`metrics.json` from the `fullsend run` output directory into the case output
directory so the orchestrator can find it.

## Files changed

| File | Change |
|------|--------|
| `internal/cli/progress.go` | Extend `RunMetrics` with `NumTurns`, `TotalCostUSD`, `InputTokens`, `OutputTokens` |
| `internal/cli/run.go` | Write `metrics.json` after all iterations, aggregating across retries |
| `internal/cli/progress_test.go` | Test metrics extraction from stream events |
| `eval/fullsend-runner.sh` | Copy `metrics.json` to case output directory |
| `eval/run-functional.sh` | Add pre-flight validation and post-run threshold checks |
| `eval/triage/cases/001-bug-url-encoding/annotations.yaml` | Add `max_turns` and `max_cost_usd` |
| `docs/ADRs/0052-functional-tests-for-agent-pipelines.md` | Add behavioral thresholds section |
| `docs/testing/functional-tests.md` | Document threshold requirements |

## Open questions

- What are reasonable initial baselines for triage? Suggest `max_turns: 15`,
  `max_cost_usd: 2.00` based on observed manual runs — generous enough to
  avoid flakiness, tight enough to catch loops.
- Should threshold violations be warnings or hard failures? This design says
  hard failures, but we could start with warnings and promote to failures
  once baselines are validated.
