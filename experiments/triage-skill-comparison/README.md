# Experiment: Triage Skill Comparison via GitHub Issue Dialogue

Related: [Issue #126 -- Story 3: Triage Agent](https://github.com/fullsend-ai/fullsend/issues/126)

Builds on [PR #169](https://github.com/fullsend-ai/fullsend/pull/169) which established the file-based simulation approach but did not incorporate questioning strategies from oh-my-claudecode or oh-my-openagent.

## Hypothesis

Existing third-party coding agent skills for interactive brainstorming and clarification can be adapted for asynchronous GitHub issue triage via comment dialogue, producing well-triaged issues from poorly-framed bug reports through iterative question-and-answer cycles. Of the available approaches, some will produce better triage outcomes than others.

## Background

Interactive coding agent skills work by asking clarifying questions in a live terminal session. Each uses a different strategy:

- **superpowers brainstorming** asks one question at a time, prefers multiple choice, uses judgment-based sufficiency (the agent decides when it understands enough to propose approaches)
- **oh-my-claudecode deep-interview** uses mathematical ambiguity gating with weighted clarity dimensions, scoring each answer and targeting the weakest dimension; deploys "challenge agent modes" (Contrarian, Simplifier, Ontologist) at round thresholds
- **oh-my-openagent Prometheus** interviews like a senior engineer in a phased approach (scope identification, deep investigation, hypothesis testing, resolution); pushes back on vague answers

The core question: **can these patterns work asynchronously over GitHub issue comments, with short-lived agents that die after each interaction?**

### Key architectural insight

Non-interactive mode (`-p` flag in both `claude` and `opencode`) is exactly the constraint we want. In interactive mode, skills use platform tools (`question`, `AskUserQuestion`) to prompt the user. In `-p` mode, there is no user -- the agent must express its output as text, which we capture and route to a GitHub issue comment. No hooks or skill modifications are needed. The skill's decision-making logic stays intact; only the I/O layer changes.

## Skills under evaluation

| # | Strategy | Source | Mechanism |
|---|----------|--------|-----------|
| 1 | `superpowers-brainstorming` | [obra/superpowers](https://github.com/obra/superpowers) | One question at a time, multiple choice preferred, YAGNI, judgment-based sufficiency |
| 2 | `omc-deep-interview` | [Yeachan-Heo/oh-my-claudecode](https://github.com/Yeachan-Heo/oh-my-claudecode) | Mathematical ambiguity gating, weighted clarity dimensions (symptom 35%, cause 30%, reproduction 20%, impact 15%), challenge modes at round thresholds |
| 3 | `omo-prometheus` | [code-yeongyu/oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent) | Phased engineer-style interview (scope, investigation, hypothesis testing), pushes for specifics, confidence-rated resolution |
| 4 | `structured-triage` | Custom (baseline) | Checklist: expected behavior, actual behavior, reproduction steps, environment, errors, frequency |
| 5 | `socratic-refinement` | Custom | Open-ended Socratic probing, follows conversational threads, discovers unstated assumptions |

Strategies 1-3 are adapted from real third-party tools. Strategies 4-5 serve as baselines -- one structured, one open-ended -- to contextualize the results.

## Experiment design

### Architecture

```
                              GitHub Issue
                             ┌─────────────┐
                             │  Title/Body  │
                             │  Comments    │
                             └──────┬───────┘
                                    │
                        ┌───────────┴───────────┐
                        │                       │
              ┌─────────▼─────────┐   ┌────────▼────────┐
              │   Triage Agent    │   │  Reporter Agent  │
              │   (single-shot)   │   │  (single-shot)   │
              │                   │   │                  │
              │ Reads full issue  │   │ Reads question,  │
              │ + all comments.   │   │ answers from     │
              │ Asks or resolves. │   │ ground truth.    │
              │ Then dies.        │   │ Then dies.        │
              └─────────┬─────────┘   └────────┬────────┘
                        │                       │
                        ▼                       ▼
                  Post question            Post answer
                  as comment               as comment
```

### Agent lifecycle

Each agent invocation is single-shot (dies after one action):

1. **Triage agent** reads the full issue (title, body, all comments), applies the configured strategy, and either posts a clarifying question or declares the issue triaged.
2. **Reporter agent** reads the issue + all comments and responds to the latest question as the user would, based on hidden ground truth.
3. The cycle repeats until the triage agent resolves or the turn limit (6) is reached.

### Simulation modes

**File-based (default):** The issue is a JSON file. Agent output is captured and appended as comments. Fully offline, reproducible, no GitHub access needed. This is what `run-experiment.sh` uses.

**GitHub-native:** Uses the `gh` CLI to create real issues and post real comments. The `github-adapter.sh` script provides this mode. Supports both auto-reply (reporter agent responds) and human-in-the-loop (wait for a real person to comment).

### Scenarios

| Scenario | Quality | What's really going on |
|----------|---------|----------------------|
| `crash-on-save` | Very poor ("app crashes when I save") | CSV import with special chars + save serializer encoding bug at >64KB payload |
| `slow-search` | Medium (some context but missing key details) | v2.3 regression: FTS5 index dropped in migration, LIKE query on 5K tasks with long descriptions |
| `auth-redirect-loop` | Good but ambiguous (detailed report, multiple possible causes) | SameSite=Strict cookie + email claim mismatch between Okta and Entra ID, affecting 30% of users |

Each scenario includes ground truth about what actually happened, which the reporter agent uses to answer questions realistically. The judge evaluates how much of this ground truth the triage agent manages to extract.

### Claude Code vs OpenCode

Both work. Set `AGENT_CLI=claude` or `AGENT_CLI=opencode`:

| Aspect | Claude Code | OpenCode |
|--------|------------|----------|
| Non-interactive mode | `claude -p "..."` | `opencode -p "..."` |
| Key constraint | No `question` tool in `-p` mode -- agent outputs text | Same |
| Skill loading | Via CLAUDE.md or plugin | Via AGENTS.md or config |

The constraint of `-p` mode is the feature: it forces the agent to express questions as output rather than using interactive tools, which is exactly the behavior needed for issue comment posting.

### Judging

An independent judge agent scores each triage on five weighted criteria:

| Criterion | Weight | What it measures |
|-----------|--------|-----------------|
| Completeness | 25% | Did the triage extract all expected information? |
| Accuracy | 25% | Is the summary consistent with ground truth? |
| Efficiency | 20% | Were turns well-spent? Any redundant questions? |
| Question quality | 15% | Were questions insightful and diagnostic? |
| Actionability | 15% | Could a developer start fixing from this summary? |

## Running the experiment

### Prerequisites

- `claude` CLI authenticated, OR `opencode` CLI configured
- `bash`, `jq`

### Quick start

```bash
# Full experiment: 3 scenarios x 5 strategies x 5 trials = 75 runs
./scripts/run-experiment.sh

# Fewer repetitions for faster iteration
./scripts/run-experiment.sh --trials 2

# Single cell
./scripts/run-experiment.sh --scenario crash-on-save --strategy omc-deep-interview

# Use OpenCode instead of Claude
./scripts/run-experiment.sh --agent opencode

# Dry run (print prompts without invoking agents)
./scripts/run-experiment.sh --dry-run

# Against live GitHub issues (requires gh CLI)
./scripts/github-adapter.sh owner/repo crash-on-save omc-deep-interview 6 claude --auto-reply
```

### Output structure

Each scenario x strategy cell runs N independent trials (default 5). The summarizer aggregates scores as mean +/- stddev across trials.

```
results/<timestamp>/
  seed-app/              # Generated TaskFlow app (optional)
  crash-on-save/
    superpowers-brainstorming/
      trial-1/
        conversation.json  # Full issue + comments
        conversation.md    # Human-readable conversation
        triage-summary.md  # Final triage output
        triage-summary.json
        judge-assessment.json
      trial-2/
        ...
      trial-N/
        ...
    omc-deep-interview/
      ...
    scenario-analysis.json # Cross-strategy comparison
  slow-search/
    ...
  auth-redirect-loop/
    ...
  summary.md             # Comparison table with rankings
```

## Results (10 trials per cell, 2026-04-06)

Full results: [`results/20260406T154300Z/summary.md`](results/20260406T154300Z/summary.md)

### Strategy rankings

| Rank | Strategy | Mean Score | Consistency (avg stddev) |
|---|---|---|---|
| 1 | omo-prometheus | 4.08 | moderate (0.50) |
| 2 | omc-deep-interview | 4.04 | low (0.63) |
| 3 | socratic-refinement | 3.74 | moderate (0.45) |
| 4 | structured-triage | 3.49 | low (0.63) |
| 5 | superpowers-brainstorming | 3.45 | moderate (0.51) |

### Key findings

1. **The top two strategies are statistically indistinguishable.** omo-prometheus (4.08) and omc-deep-interview (4.04) are within noise of each other. Both outperform the baselines, but neither dominates.

2. **Consistency matters as much as mean score.** omc-deep-interview and structured-triage both show high variance (stddev >1.2 on crash-on-save), meaning they occasionally produce excellent results but also fail badly — including JSON parse failures that zero out the triage summary. A strategy that scores 3.5 reliably may be preferable to one that scores 4.0 on average but sometimes produces 1.85.

3. **All strategies struggle with multi-causal bugs.** On auth-redirect-loop (two interacting root causes: SameSite cookie + email claim mismatch), every strategy averaged below 3.6. Most resolved after a single exchange, consistently missing the primary root cause (SameSite=Strict). This is the hardest scenario and the one most representative of real-world triage.

4. **Premature resolution is the dominant failure mode.** Across all strategies and scenarios, agents overwhelmingly resolve after 1 turn. The single-turn strategies (socratic-refinement, superpowers-brainstorming) are especially prone, but even multi-turn strategies often stop early. The current prompt framing may be biasing agents toward closure.

5. **Easy scenarios flatten differences.** On slow-search, four of five strategies scored above 4.3. The scenario is "easy" — a single root cause with clear diagnostic signals — and doesn't discriminate well between strategies.

6. **Question quality is universally high.** Even low-scoring trials tend to have good questions (4/5). The problem is rarely *what* agents ask — it's that they don't ask *enough* before resolving.

### Recommendations for Story 3 (issue #126)

Based on these results, here is what we think should inform the triage agent design:

1. **Don't pick a single strategy — use a hybrid.** The phased interview structure from omo-prometheus and the ambiguity gating from omc-deep-interview both contribute value. A production triage agent should combine a phased approach (scope → investigate → hypothesize → resolve) with an explicit "am I confident enough to close?" gate that prevents premature resolution.

2. **Add a resolution threshold.** The biggest lever for improvement is not question quality — it's preventing premature closure. The triage agent should be required to meet a minimum confidence or information coverage threshold before it can resolve. The omc-deep-interview approach of scoring clarity dimensions is a good starting point, though its mathematical framing didn't reliably prevent early closure in practice.

3. **Design for 2-3 turns, not 1.** Most value comes from the second question. Agents that used 2 turns consistently outscored those that used 1. The system prompt or strategy should encourage at least one follow-up before resolution, unless the issue is unambiguous. A simple heuristic: if the agent's own triage summary lists information gaps, it should not resolve.

4. **Test with multi-causal scenarios.** The auth-redirect-loop scenario exposed weaknesses that the other two didn't. Real production bugs often have interacting causes. The triage agent's test suite should include scenarios where the obvious first hypothesis is incomplete.

5. **Address the JSON parse failure mode.** Several trials (especially omc-deep-interview and structured-triage) failed because the agent produced unparseable output, resulting in scores of ~2.0. The production agent needs robust output parsing and should fall back gracefully rather than losing all gathered information.

6. **The experiment framework itself is ready for reuse.** The file-based simulation, multi-trial aggregation, and judge scoring rubric can be used to evaluate the production triage agent as it develops. Run the same scenarios against the real implementation to track progress.

## Future work

This experiment has been succeeded by [triage-strategy-evaluation](../triage-strategy-evaluation/), which addresses methodological limitations identified in this study: too few scenarios, unrealistic reporter behavior, same-model judging, premature resolution bias in the prompt framing, unequal adapter effort, and a self-contradictory efficiency criterion. See the v2 README for the full list of changes.

## Design decisions

### Why file-based simulation?

1. The experiment environment may lack GitHub API access.
2. File-based simulation is reproducible and debuggable.
3. Converting to GitHub API is mechanical: swap `jq` reads/writes for `gh api` calls. The `github-adapter.sh` shows how.
4. The prompts and strategies are the intellectual core; the I/O layer is plumbing.

### Why `-p` mode rather than hooks?

Non-interactive mode naturally forces the behavior we want. No skill modifications, no hook wiring, no complexity. The agent outputs its question as text because it has no other choice, and we route that text to the right place.

### Why adapt strategies into prompt fragments rather than loading actual skills?

The actual skills are designed for interactive sessions with tool access. Loading them in `-p` mode would be fighting the system. Instead, we extract each skill's questioning strategy, decision logic, and sufficiency criteria into prompt fragments (adapters) that encode the same intellectual approach without depending on interactive tools. This is a deliberate architectural choice: the adapter is the skill's brain transplanted into a different body.

### Turn limit

6 turns max (3 triage questions, 3 reporter answers, plus possible forced resolution). This prevents runaway loops while allowing enough dialogue for meaningful triage. The limit is configurable via `--max-turns`.

## Extending the experiment

### Adding a new strategy

1. Create `adapters/my-strategy.md` following existing patterns.
2. Add the name to the `STRATEGIES` array in `run-experiment.sh`.

### Adding a new scenario

1. Create `scenarios/my-scenario.json` with `title`, `body`, `quality`, `ground_truth`, and `expected_triage_extracts`.
2. Add the name to the `SCENARIOS` array in `run-experiment.sh`.

### Converting to live GitHub

Use `github-adapter.sh` directly, or integrate with GitHub Actions:

```yaml
on:
  issues:
    types: [opened, labeled]
  issue_comment:
    types: [created]
```

A webhook-triggered workflow could replace the polling loop, starting a new triage agent invocation each time the reporter comments. The adapter script has both `--auto-reply` (reporter agent) and human-in-the-loop modes.

## File index

| File | Purpose |
|------|---------|
| `adapters/superpowers-brainstorming.md` | Strategy: one question, multiple choice, YAGNI |
| `adapters/omc-deep-interview.md` | Strategy: ambiguity scoring, challenge modes (from oh-my-claudecode) |
| `adapters/omo-prometheus.md` | Strategy: phased engineer interview (from oh-my-openagent) |
| `adapters/structured-triage.md` | Strategy: checklist baseline |
| `adapters/socratic-refinement.md` | Strategy: open-ended Socratic probing |
| `scenarios/crash-on-save.json` | Very poor bug report |
| `scenarios/slow-search.json` | Medium quality bug report |
| `scenarios/auth-redirect-loop.json` | Good but ambiguous report |
| `prompts/triage-system.md` | Base triage agent prompt |
| `prompts/reporter-system.md` | Reporter agent prompt |
| `prompts/judge-system.md` | Judge agent scoring rubric |
| `prompts/seed-app.md` | Prompt to generate the fictional TaskFlow app |
| `prompts/scenario-analysis-system.md` | Cross-strategy analysis prompt |
| `scripts/run-experiment.sh` | Main orchestrator |
| `scripts/run-single-trial.sh` | Single scenario x strategy dialogue loop |
| `scripts/judge.sh` | Judge agent invocation |
| `scripts/analyze-scenario.sh` | Cross-strategy scenario analysis |
| `scripts/summarize.sh` | Summary table generator (multi-trial aggregation) |
| `scripts/github-adapter.sh` | Live GitHub issue adapter |
