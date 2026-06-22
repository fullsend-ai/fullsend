# Functional Tests

Functional tests exercise the full agent pipeline — pre-script, agent
execution, post-script — against ephemeral GitHub fixtures. They verify that
agents produce the right side effects (labels, comments, PR state) when given
controlled inputs.

For the decision rationale, see
[ADR 0052](../ADRs/0052-functional-tests-for-agent-pipelines.md). For the
framework choice, see
[ADR 0051](../ADRs/0051-agent-eval-harness-for-test-infrastructure.md). For
the broader testing problem, see
[testing-agents.md](../problems/testing-agents.md).

## agent-eval-harness

Functional tests are built on
[agent-eval-harness](https://github.com/opendatahub-io/agent-eval-harness),
a generic evaluation framework for agents and skills. We use it for test case
management, judge orchestration, scoring, and threshold gating so we don't
build test infrastructure ourselves.

The integration points are lifecycle hooks and the
[opaque CLI runner contract](https://github.com/opendatahub-io/agent-eval-harness/blob/main/docs/opaque-cli-runner-contract.md).
The harness drives case iteration, invoking `before_each` hooks (create
ephemeral repo and fixture), the CLI runner (`eval/scripts/run-fullsend.sh`
which calls `fullsend run`), and `after_each` hooks (capture fixture state,
delete ephemeral repo). The harness then invokes judges, computes scores,
and enforces thresholds.

The harness is vendored as a git submodule at `eval/.agent-eval-harness`.
Dependabot keeps it updated automatically. After cloning, run
`git submodule update --init` to check it out.

When adding test capabilities (new judge types, dataset generation, regression
detection), check whether agent-eval-harness already supports it or can be
extended upstream before building something fullsend-specific.

## Prerequisites

- Go toolchain (to build `fullsend`)
- `gh` CLI, authenticated
- A GitHub org for test fixtures (`EVAL_ORG`)
- GCP credentials with Vertex AI access (`GOOGLE_APPLICATION_CREDENTIALS`)
- Anthropic project ID (`ANTHROPIC_VERTEX_PROJECT_ID`)

## Running tests

```bash
make functional-tests
```

This builds the `fullsend` binary, iterates over test cases, and scores each
one. Results are printed to stdout with pass/fail per judge and threshold.

### Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `EVAL_ORG` | Yes | GitHub org where ephemeral fixture repos are created |
| `GH_TOKEN` | Yes | GitHub token with repo/org permissions in `EVAL_ORG` |
| `GOOGLE_APPLICATION_CREDENTIALS` | Yes | Path to GCP credentials JSON |
| `ANTHROPIC_VERTEX_PROJECT_ID` | Yes | GCP project with Vertex AI access |
| `GOOGLE_CLOUD_PROJECT` | Yes | GCP project ID |
| `CLOUD_ML_REGION` | Yes | GCP region for Vertex AI (e.g. `us-central1`) |
| `FULLSEND_DIR` | No | Path to fullsend scaffold directory (default: `internal/scaffold/fullsend-repo`) |
| `EVALS_HOST_CREDENTIALS` | No | Path to host GCP credentials for scoring (CI only — overrides sandbox-rewritten creds) |

## Directory layout

```
eval/
  run-functional.sh           # Orchestrator: workspace -> execute -> score
  scripts/
    setup-fixture.sh          # before_each hook: create ephemeral repo + fixture
    run-fullsend.sh           # CLI runner: call fullsend run with env vars
    capture-fixture.sh        # after_each hook: snapshot fixture state
    teardown-fixture.sh       # after_each hook: delete ephemeral repo
  <skill>/                    # One directory per agent skill
    eval.yaml                 # Test config: judges, thresholds, models
    cases/
      001-<name>/
        input.yaml            # Fixture definition (forge, type, title, body)
        annotations.yaml      # Expected state + rubric hints for LLM judge
        repo/                 # Source tree the agent sees (or symlink)
    repos/                    # Shared repo content, symlinked by cases
```

## Writing a test case

### 1. Create the case directory

```bash
mkdir -p eval/<skill>/cases/<NNN>-<short-name>
```

Number cases sequentially within each skill.

### 2. Write `input.yaml`

Define the GitHub fixture the agent will triage or review:

```yaml
forge: github
fixture: issue          # or: pull_request
title: "Bug: login fails with special characters"
body: |
  When a username contains a `+`, the login form rejects it
  with a 400 error.
```

### 3. Write `annotations.yaml`

Describe the expected outcome. This serves two purposes: deterministic checks
(labels, state) and hints for the LLM judge.

```yaml
labels:
  required:
    - bug
    - triage/accepted
max_turns: 15
max_cost_usd: 2.00
triage_expectations:
  - Agent should read the validation regex in src/auth/validators.py
  - Agent should notice the regex already handles `+` characters
  - Comment should reference the specific regex pattern
```

### 4. Add repo content

Either create a `repo/` directory with the source files the agent will see, or
symlink to a shared repo under `eval/<skill>/repos/`:

```bash
ln -s ../../repos/python-webapp eval/<skill>/cases/<NNN>-<short-name>/repo
```

### 5. Configure judges in `eval.yaml`

Each skill's `eval.yaml` defines judges (LLM-graded or deterministic) and
pass thresholds. See `eval/triage/eval.yaml` for a working example.

## Behavioral thresholds

Every test case must declare behavioral thresholds in `annotations.yaml`:

```yaml
max_turns: 15
max_cost_usd: 2.00
```

These are mandatory — `make lint-eval-cases` validates their presence, and the
`max_turns` and `max_cost` deterministic judges in `eval.yaml` compare the
agent's actual metrics (from `metrics.json`, written by `fullsend run`) against
these thresholds. A case that passes all quality judges but exceeds a behavioral
threshold is a failure.

### Why these two metrics

- **`max_turns`** — the most intuitive measure of agent efficiency. A turn is
  one assistant response. Excessive turns usually mean the agent is looping,
  retrying, or taking an indirect path.

- **`max_cost_usd`** — captures token usage indirectly but accounts for model
  pricing differences. Cost is what we actually care about controlling.

Raw token counts (`input_tokens`, `output_tokens`) are recorded in
`metrics.json` but not gated. Token counts vary with caching behavior and
prompt structure in ways that are hard to baseline. Cost already captures
tokens. When statistical evals provide per-model token distributions, token
thresholds can be added as a refinement.

### Setting baselines

Start generous and tighten. Watch a few manual runs to see typical turn counts
and costs, then set thresholds at roughly 2x the observed values. The goal is
to catch regressions (looping agents, model changes that spike cost) without
causing flaky failures from normal variance.

When statistical evals are available (tracked in
[#73](https://github.com/fullsend-ai/fullsend/issues/73)), observed
distributions will inform tighter baselines.

## Scoring

Two types of judges score each case:

- **LLM judge** — an LLM evaluates the agent's work against the
  `annotations.yaml` rubric on a 1-5 scale. Gated on `min_mean`.
- **Deterministic checks** — Python expressions that verify specific
  properties of the captured fixture state (e.g., required labels present).
  Gated on `min_pass_rate`.

Threshold-based gating acknowledges non-determinism. A `min_mean: 2.5` means
the agent must score at least 2.5 averaged across runs, not that every run
must score 2.5.

## CI integration

Functional tests run in GitHub Actions when files under `eval/` or
`internal/scaffold/` change. The workflow is defined in
`.github/workflows/functional-tests.yml`.

Tests require the `evals` GitHub environment, which provides secrets
(`EVAL_GH_TOKEN`, `GCP_CREDENTIALS`) and vars (`EVAL_ORG`,
`ANTHROPIC_VERTEX_PROJECT_ID`).
