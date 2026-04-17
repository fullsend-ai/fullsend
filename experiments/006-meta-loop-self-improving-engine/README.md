# Experiment 006: Zero-Config Autonomous Bug Fix Engine with Self-Improving Meta-Loop

**Date:** 2026-03-29
**Status:** Complete

See [RESULTS.md](RESULTS.md) for the detailed run-by-run timeline, human-vs-engine comparison, and analysis.

## Hypothesis

An autonomous bug fix engine can operate on **any GitHub repository without requiring the target repo to change anything** — no GitHub App installation, no config files, no labels, no permissions setup. The engine is a self-contained GitHub Actions workflow: point it at an issue URL and it reads the issue, clones the repo, analyzes the code, writes a fix, self-reviews it, and opens a cross-fork PR. The target repo owners don't install anything. A PR shows up like any other contribution.

A secondary hypothesis: if the engine produces structured execution artifacts from every run, an LLM can read those artifacts and produce targeted patches to the engine itself — turning production execution into a self-improvement signal without human-written evals or curated benchmarks.

## Background

Most agent tooling requires the target repository to opt in. You install a GitHub App, add a configuration file, label issues a certain way, or configure permissions before the agent can operate. This creates an adoption barrier: every repo needs setup work, and the repo owners need to decide to participate.

This experiment removes that requirement entirely. The engine is a single GitHub Actions workflow that contains the complete bug fix pipeline. You give it an issue URL and a fork to push to. It reads the issue and codebase through public APIs and git, operates on a repository it has never seen before, and opens a cross-fork PR using the same workflow target repo owners already have for human contributors. The target repo doesn't know or care that an agent produced the PR.

The engine was then improved using its own production results. A separate local script (the "meta-loop") triggers full engine runs in CI, downloads the structured execution artifacts when they complete, feeds them to an LLM for diagnosis, applies the LLM's patches to the engine source, and re-triggers. The meta-loop doesn't fix the target bug — it fixes the *engine* so the next full run gets the target bug right.

This touches several fullsend problem areas:
- **[Repo Readiness](../../docs/problems/repo-readiness.md)** — the engine operates on repos with no prior setup, testing the lower bound of what's possible without repo-side preparation
- **[Agent Architecture](../../docs/problems/agent-architecture.md)** — the engine uses a phased pipeline (triage → implement → review → validate → report) with backtracking, demonstrating one model for structuring agent authority and interaction
- **[Testing the Agents](../../docs/problems/testing-agents.md)** — the meta-loop is an alternative to golden-set evaluation: instead of curated test cases, production execution provides the signal
- **[Production Feedback](../../docs/problems/production-feedback.md)** — the entire experiment is a concrete instance of production signals feeding back into agent improvement

## Artifacts

Everything described below is public.

| Artifact | Link |
|----------|------|
| Engine repository | [ascerra/rl-bug-fix-full-send](https://github.com/ascerra/rl-bug-fix-full-send) |
| Target issue | [nonflux/build-definitions#1](https://github.com/nonflux/build-definitions/issues/1) |
| First successful run → PR #3 | [nonflux/build-definitions#3](https://github.com/nonflux/build-definitions/pull/3) |
| Second successful run → PR #4 | [nonflux/build-definitions#4](https://github.com/nonflux/build-definitions/pull/4) |
| Auto-fix #1: scope creep | [`e06bd71`](https://github.com/ascerra/rl-bug-fix-full-send/commit/e06bd71) |
| Auto-fix #2: truncation 5k→50k | [`4e2623b`](https://github.com/ascerra/rl-bug-fix-full-send/commit/4e2623b) |
| Auto-fix #3: missing git commit | [`f13e984`](https://github.com/ascerra/rl-bug-fix-full-send/commit/f13e984) |
| Auto-fix #4: unique branch names | [`1a1c56b`](https://github.com/ascerra/rl-bug-fix-full-send/commit/1a1c56b) |
| All workflow runs | [ascerra/rl-bug-fix-full-send/actions](https://github.com/ascerra/rl-bug-fix-full-send/actions) |

## Setup

### The engine

The engine lives entirely in a single repository. No code, configuration, or integration touches the target repo. The operator provides an issue URL and a fork — the engine handles everything else: cloning the target, reading the issue, analyzing the codebase, writing a fix, reviewing its own fix, and opening a cross-fork PR that lands on the target repo like any human contribution.

Internally, five phases execute in sequence — **triage → implement → review → validate → report** — with backtracking (review can reject back to implement) and a CI remediation sub-loop after PR creation. Each phase implements an OODA cycle (observe → plan → act → validate → reflect). A **neutral observer** job runs independently after the engine, reconstructing the execution from artifacts, cross-checking claims, generating attestations, and enforcing policy.

The engine was built using a ralph loop following a predefined `IMPLEMENTATION_PLAN.md`.

### The meta-loop

A local script (`meta-loop.sh` + `meta_loop_agent.py`) that orchestrates the feedback cycle:

```
[local machine]                              [GitHub Actions]
meta-loop.sh                                 RL Bug Fix Engine
    │                                             │
    ├─ trigger workflow ─────────────────────────►│ full e2e bug fix run
    │                                             │ (triage → implement → review
    │                                             │  → validate → push PR → report)
    │◄─ download execution artifacts ─────────────┤
    │                                             │
    ├─ LLM reads full trace                       │
    │  (how the engine reasoned, what it tried,   │
    │   what the fix looked like, why it failed)  │
    │                                             │
    ├─ patch engine source code                   │
    ├─ push changes                               │
    ├─ trigger next workflow ────────────────────►│ another full e2e bug fix run
    │                                             │
    └─ repeat until success                       │
```

Each "trigger workflow" kicks off a **complete, independent bug fix attempt** — the whole pipeline from scratch against the target issue. The meta-loop doesn't fix the bug; it fixes the *engine* so the next full run gets it right.

### The target

[nonflux/build-definitions#1](https://github.com/nonflux/build-definitions/issues/1) — an intermittent race condition in a Tekton StepAction where parallel image processing used shared temp directories, causing `lstat: no such file or directory` errors.

### Invocation

```bash
./scripts/meta-loop.sh \
  --issue-url "https://github.com/nonflux/build-definitions/issues/1" \
  --fork-repo "ascerra/build-definitions" \
  --provider gemini \
  --continuous \
  --max-runs 10 \
  --auto-push
```

## Key findings

Four autonomous self-corrections took the engine from "fails on every real repo" to "produces correct PRs in a single pass" in ~90 minutes of wall clock. The engine's first PR was then graded against the real human fix for the same bug — it matched the human's strategy and arrived at it in 2.8 minutes, with better documentation but slightly less precise code. See [RESULTS.md](RESULTS.md) for the full breakdown.

| Auto-fix | Category | What the LLM identified |
|----------|----------|----------------------|
| #1 | Prompt design | The implement prompt needed scope constraints when the review agent flags drift |
| #2 | Context window | 5k char file limit was too small for real-world files |
| #3 | Missing workflow step | The implement agent wrote files but never committed them |
| #4 | State management | Hardcoded branch names collide on repeated runs |

## Limitations

1. **Single target issue.** The meta-loop ran against one bug in one repository. The self-improvement pattern may not generalize to harder bugs, larger codebases, or repos with more complex CI pipelines.

2. **The meta-loop runs locally.** The feedback script requires a human to launch it and a local machine with LLM access. A fully autonomous version would run the meta-loop itself in CI, which introduces the recursive problem of the meta-loop needing its own meta-loop.

3. **No adversarial testing.** The target repo is benign. The engine's `shell_run` on target repos is an inherent RCE surface — a malicious repository (or crafted issue) could trigger dangerous commands. There's no formal sandbox (no container isolation, no seccomp, no namespace separation). This is relevant to the [security threat model](../../docs/problems/security-threat-model.md).

4. **No contract tests for external APIs.** All Gemini, Anthropic, and GitHub API interactions are tested through mocks. The meta-loop catches production failures, but the feedback cycle is slow.

5. **Engine architecture has known technical debt.** The `PipelineEngine` is a god object handling orchestration, backtracking, escalation, CI monitoring, and execution recording. Duplicated constants across modules are drift hazards. The HTML report has metric bugs (shows zeros for files modified and tests run).

## What this means for fullsend

### Agents don't need the target repo's permission to contribute

The most significant finding isn't the meta-loop — it's that the engine works on repos that never opted in. The target repository had no agent configuration, no bot integrations, no special labels, and no awareness that an engine existed. A PR appeared like any other open-source contribution. The maintainers review it using their existing workflow.

This reframes the [repo readiness](../../docs/problems/repo-readiness.md) problem. The current framing assumes repos need preparation before agents can operate on them — test coverage, CI maturity, configuration. This experiment shows agents can start contributing with zero repo-side readiness. A team doesn't need to have "agentified" their codebase before agents can work on it.

The tradeoff is real: without repo-specific context (no AGENTS.md, no CLAUDE.md, no contribution guidelines awareness, no CI configuration knowledge), the agent operates purely from issue text and source code. Quality and appropriateness improve with context — and files like AGENTS.md aren't a bad thing. They're how a team iteratively improves agent contributions over time: each round of agent output reveals what context was missing, and the team adds it. But that's an optimization, not a prerequisite. The floor isn't "can't contribute at all" — it's "contributes like a competent stranger who gets better as the team gives it more context."

### Production execution as evaluation

The meta-loop offers an alternative to the golden-set evaluation approach explored in [Experiment 004](../promptfoo-eval/README.md). Instead of curated test cases with expected outputs, the signal comes from running the agent against real tasks and feeding the results back. The two approaches are complementary:

- **Golden-set (promptfoo):** catches prompt regressions, verifies format compliance, runs in seconds. Tests prompts, not agents.
- **Meta-loop:** catches integration-level bugs that only manifest in production, verifies end-to-end behavior, takes minutes to hours. Tests the full agent system.

Neither replaces the other. A robust agent testing strategy likely needs both — golden-set as the fast feedback layer (CI on every commit) and production-artifact feedback as the slow but comprehensive layer (periodic or on failure).

### Self-improvement has a recursion problem

The meta-loop improves the engine using an LLM, but who improves the meta-loop? The meta-loop script itself had bugs that required manual fixes. A meta-meta-loop is theoretically possible but practically unwieldy. At some point, a human needs to be in the loop — the question is where. This connects to the [autonomy spectrum](../../docs/problems/autonomy-spectrum.md) question: the meta-loop pushes the human intervention point further out, but doesn't eliminate it.

## Relationship to other experiments

- **[Experiment 001](../adr46-scanner/README.md)** and **[Experiment 002](../adr46-claude-scanner/README.md)** test detecting architectural drift — a concern that the meta-loop's engine doesn't address (it fixes bugs, not architectural violations).
- **[Experiment 003](../003-agent-outage-fire-drill.md)** tests whether humans maintain capability when agents are removed. The meta-loop experiment is the other side: what happens when you push agents further toward full autonomy, including self-repair?
- **[Experiment 004](../promptfoo-eval/README.md)** tests prompt-level evaluation with promptfoo. The meta-loop provides a complementary evaluation approach at the integration level.
