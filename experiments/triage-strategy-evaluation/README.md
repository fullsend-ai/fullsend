# Experiment: Triage Strategy Evaluation (v2)

Related: [Issue #126 — Story 3: Triage Agent](https://github.com/fullsend-ai/fullsend/issues/126)

Successor to the [triage-skill-comparison](../triage-skill-comparison/) experiment, which compared five questioning strategies across three scenarios. That study produced directionally useful results but had methodological limitations that this experiment addresses.

## What changed from v1

| Concern | v1 behavior | v2 fix |
|---------|------------|--------|
| Too few scenarios | 3 scenarios, rankings sensitive to individual scenario fit | 10 scenarios spanning 7 bug archetypes |
| Unrealistic reporter | Perfect recall, always cooperative | Reporter has a realism profile: sometimes uncertain, forgetful, or verbose with irrelevant details |
| Same model judges itself | Triage agent and judge share blind spots | `--judge-model` flag; default judge uses a different model than the triage agent |
| Premature resolution bias | Binary "ASK or RESOLVE" with no friction on resolve | Reframed: agent must justify readiness to resolve and self-check against its own information gaps |
| Unequal adapter effort | Adapters varied 20-84 lines; baselines were intentionally simple | All adapters normalized to ~40-50 lines with the same structural template |
| Efficiency criterion contradicts itself | Penalizes both wasted turns and premature closure, with no clear boundary | Split into **thoroughness** (did it ask enough?) and **economy** (did it waste turns?) |
| Parse failures distort scores | JSON formatting errors counted as strategy failures | Separate **reliability** metric; parse failures excluded from quality scores |
| Single correct interpretation | Ground truth has one root cause hierarchy | Judge evaluates against a set of **acceptable diagnostic paths**, not just the canonical one |

## Hypothesis

The same hypothesis as v1 — that interactive questioning strategies can be adapted for asynchronous GitHub issue triage — but with a sharper question: **which strategy properties matter, and under what conditions?**

Specifically:
1. Do phased/gated strategies (omo-prometheus, omc-deep-interview) outperform ungated ones (socratic-refinement, structured-triage) when the reporter is realistic rather than ideal?
2. Does the v1 finding of "premature resolution as the dominant failure mode" persist when the prompt framing no longer biases toward closure?
3. Do strategy rankings change across bug archetypes, or is one strategy universally better?

## Scenarios (10)

| # | Scenario | Archetype | Quality | Causal structure |
|---|----------|-----------|---------|-----------------|
| 1 | `crash-on-save` | Data/encoding | Very poor | Single cause, size-dependent trigger |
| 2 | `slow-search` | Performance regression | Medium | Single cause, clear diagnostic signal |
| 3 | `auth-redirect-loop` | Auth/integration | Good but ambiguous | Two interacting causes |
| 4 | `silent-data-corruption` | Data integrity | Medium | Subtle regex bug, batch process |
| 5 | `flaky-ci` | Testing/CI | Poor | Environment-dependent, non-deterministic |
| 6 | `memory-leak` | Resource/performance | Good (has metrics) | Gradual degradation, needs profiling |
| 7 | `intermittent-403` | Infrastructure/deployment | Medium | Stale deployment on 1-of-N servers |
| 8 | `email-delay` | Queue/integration | Medium-good | Priority inversion in queue |
| 9 | `file-upload-corruption` | Data/encoding | Poor | Size-dependent path split |
| 10 | `wrong-search-results` | Logic/data | Medium | Schema mapping inversion after migration |

Scenarios 1-3 are carried over from v1 (ground truth unchanged) for continuity. Scenarios 4-10 are new and chosen to cover archetypes that v1 missed: batch processing bugs, CI/infrastructure issues, gradual degradation, and logic errors.

### Scenario design principles

- **Multiple acceptable diagnostic paths.** Each scenario's ground truth includes `acceptable_paths` — alternative valid triage conclusions that are not the canonical root cause but would still lead a developer in a productive direction.
- **Reporter realism profiles.** Each scenario specifies a `reporter_profile` controlling how the simulated reporter behaves (see Reporter realism below).
- **Difficulty calibration.** Scenarios are tagged with expected difficulty (easy / medium / hard) based on how many questions an ideal triager would need.

## Strategies (5, normalized)

Same five strategies as v1, but with normalized adapters:

| # | Strategy | Source | Core mechanism |
|---|----------|--------|---------------|
| 1 | `superpowers-brainstorming` | obra/superpowers | One question, multiple choice, YAGNI, judgment-based sufficiency |
| 2 | `omc-deep-interview` | oh-my-claudecode | Clarity dimension scoring, ambiguity gating, challenge modes |
| 3 | `omo-prometheus` | oh-my-openagent | Phased engineer interview, pushes back on vague answers |
| 4 | `structured-triage` | Custom baseline | Checklist: expected/actual behavior, steps, environment, errors, frequency |
| 5 | `socratic-refinement` | Custom baseline | Open-ended Socratic probing, follows conversational threads |

### Adapter normalization

All adapters follow the same template:
1. **Approach** (1 paragraph) — what this strategy does differently
2. **Questioning rules** (4-6 numbered rules)
3. **Sufficiency criteria** — when to resolve
4. **When to stop asking** — when further questions have diminishing returns

Target length: 40-50 lines each. No adapter includes output format extensions (like `clarity_scores` or `confidence`) — those were removed because they conflated strategy evaluation with output format compliance.

## Key design changes

### Reporter realism

The reporter agent receives a `reporter_profile` from the scenario that controls its behavior:

| Profile | Behavior |
|---------|----------|
| `cooperative` | Answers fully from ground truth (v1 behavior, used sparingly as a control) |
| `typical` | Sometimes says "I'm not sure" or "I don't remember exactly"; gives partial answers to broad questions; occasionally volunteers irrelevant details |
| `difficult` | Misremembers some details; gives confidently wrong information on non-critical points; needs specific questions to surface key details; gets frustrated with overly technical questions |

Most scenarios use `typical`. One or two use `cooperative` (easy scenarios) and one or two use `difficult` (hard scenarios).

### Resolve/ask reframing

v1's triage prompt said:

> Decide: ASK a clarifying question or RESOLVE the issue.

v2 replaces this with:

> Your default action is to ask a clarifying question. You should only resolve when you are confident you have enough information for a developer to act without contacting the reporter again. Before resolving, review your own information gaps — if you have listed gaps that a question could fill, you should ask rather than resolve.

This makes "ask" the default and "resolve" the marked choice, reducing the bias toward premature closure observed in v1.

### Judge improvements

1. **Separate model.** The `--judge-model` flag allows using a different model (default: `claude-sonnet-4-6`) so the judge doesn't share the triage agent's blind spots.
2. **Split efficiency into thoroughness + economy.** Thoroughness asks "did it ask enough before resolving?" Economy asks "did it waste turns?" These are no longer in tension.
3. **Reliability metric.** Parse failures and malformed output are tracked separately. Trials where the agent produced unparseable output are flagged and excluded from quality scores, with a separate reliability rate reported per strategy.
4. **Acceptable diagnostic paths.** The judge receives not just the canonical root cause but a set of acceptable alternative conclusions. A triage that identifies a valid alternative path scores well on accuracy even if it doesn't match the canonical cause exactly.

### Scoring rubric (revised)

| Criterion | Weight | What it measures |
|-----------|--------|-----------------|
| Completeness | 25% | Did the triage extract the expected information? |
| Accuracy | 25% | Is the summary consistent with ground truth or an acceptable path? |
| Thoroughness | 15% | Did the agent ask enough questions before resolving? Were there obvious follow-ups it skipped? |
| Economy | 10% | Were turns well-spent? Any redundant or low-value questions? |
| Question quality | 15% | Were questions insightful and diagnostic? |
| Actionability | 10% | Could a developer start fixing from this summary? |

**Reliability** (parse success rate) is reported separately, not included in the weighted total.

## Running the experiment

### Prerequisites

- `claude` CLI authenticated, OR `opencode` CLI configured
- `bash`, `jq`

### Quick start

```bash
# Full experiment: 10 scenarios x 5 strategies x 5 trials = 250 runs
./scripts/run-experiment.sh

# Fewer trials for faster iteration
./scripts/run-experiment.sh --trials 2

# Single cell
./scripts/run-experiment.sh --scenario crash-on-save --strategy omc-deep-interview

# Use a different model for judging
./scripts/run-experiment.sh --judge-model claude-sonnet-4-6

# Resume a partially completed run
./scripts/run-experiment.sh --resume results/20260407T010118Z

# Dry run (print prompts without invoking agents)
./scripts/run-experiment.sh --dry-run
```

### Output structure

```
results/<timestamp>/
  seed-app/
  crash-on-save/
    superpowers-brainstorming/
      trial-1/
        conversation.json
        conversation.md
        triage-summary.json
        triage-summary.md
        judge-assessment.json
      trial-2/
        ...
    scenario-analysis.json
  slow-search/
    ...
  summary.md
```

## Results (2026-04-07, N=10 trials per cell)

Full results: [`results/20260407T010118Z/summary.md`](results/20260407T010118Z/summary.md)

### Strategy rankings

| Rank | Strategy | Mean score | Reliability |
|------|----------|-----------|-------------|
| 1 (tie) | omo-prometheus | 4.38 | 98% |
| 1 (tie) | omc-deep-interview | 4.38 | 97% |
| 3 | socratic-refinement | 4.32 | 100% |
| 4 | structured-triage | 3.89 | 99% |
| 5 | superpowers-brainstorming | 3.87 | 100% |

Scores are weighted totals (1–5 scale) across completeness (25%), accuracy (25%), thoroughness (15%), question quality (15%), economy (10%), and actionability (10%). Each strategy was evaluated against 10 scenarios × 10 trials = 100 runs, with parse failures excluded from quality scores.

### What separates the top from the bottom

The 0.5-point gap (~12%) between the top three and bottom two is not about asking more questions — it is about asking *better* ones. The top strategies share three habits:

1. **Hypothesis-first questioning.** omo-prometheus and omc-deep-interview propose falsifiable hypotheses and ask reporters to test them ("look for yellow warning triangles next to the Set-Cookie header", "toggle the filter and search again"). This converts a single exchange into both data collection and hypothesis elimination. The bottom two strategies gather symptoms without proposing mechanisms.

2. **Causal dating.** Top strategies ask "when did this start?" early and immediately connect the answer to a code change or deployment event. omo-prometheus caught the phrase "like I always do" and inferred a regression — then asked what changed. structured-triage asks "when" but treats it as a checklist item rather than a causal pivot.

3. **Dual-structure questions.** Top strategies pair competing hypotheses in a single turn ("does it follow the user or the browser?"), eliminating a class of causes per exchange. Bottom strategies ask one dimension at a time.

### Scenario-specific patterns

No single strategy dominates everywhere, but the patterns are consistent enough to be useful:

| Bug class | Best strategy | Worst | Gap | Why |
|-----------|--------------|-------|-----|-----|
| Regression (version-triggered) | omo (4.75) | structured (4.04) | 0.71 | Requires immediate causal dating; omo links version → code path in ≤3 turns |
| Multi-cause (auth + cookies) | omc (4.12) | superpowers (3.31) | 0.81 | Needs hypothesis layering; omc isolates compounding issues |
| Intermittent/infra | omc (4.66) | superpowers (3.83) | 0.83 | Requires systematic-vs-edge-case distinction |
| Silent corruption | omo (4.83) | superpowers (4.61) | 0.22 | All strategies do well; high-leverage questions give a slight edge |

The widest gaps appear on multi-cause and intermittent bugs — exactly the scenarios where checklist-style triage resolves prematurely.

### Premature resolution (v1 finding revisited)

v1 found premature resolution as the dominant failure mode. v2's reframed prompt ("your default action is to ask") reduced but did not eliminate it. structured-triage still resolves after 2–3 questions despite having clear follow-ups available, scoring 2.0–3.0 on thoroughness in its worst trials. The top strategies avoid this by self-checking: omo-prometheus explicitly reviews its information gaps before resolving.

### Implications for Story 3 (Triage Agent)

These results point to several design consequences for the triage agent described in [Issue #126](https://github.com/fullsend-ai/fullsend/issues/126):

**1. Strategy matters more than model choice.** The 0.5-point quality gap between strategies is larger than typical model-to-model variation at the same capability tier. The triage agent's prompt adapter is a higher-leverage design choice than which model it runs on.

**2. Adopt hypothesis-driven prompting, not checklist prompting.** The structured-triage approach (expected behavior → actual behavior → steps → environment) maps to a traditional bug template, but it consistently underperforms strategies that form and test hypotheses. The triage agent should be prompted to propose a mechanism ("I think X is happening because Y — can you check Z?") rather than collect fields.

**3. The "information sufficiency" check needs teeth.** Story 3's acceptance criteria say "assesses information sufficiency." The experiment shows that simply listing information gaps is not enough — structured-triage lists gaps but resolves anyway. The prompt should require the agent to explain *why* each remaining gap is not worth pursuing before it can resolve, similar to omo-prometheus's self-check.

**4. Budget 3–4 exchanges, not 1.** The top strategies achieve their best scores in 3–4 turns. A triage agent that resolves on first read (the single-shot model) will miss the multi-cause and intermittent bugs where the gap between good and bad triage is largest. The asynchronous comment-based dialogue modeled in this experiment maps directly to GitHub issue comments.

**5. Reliability is a non-issue at this scale.** All strategies achieved ≥97% parse reliability. The triage agent does not need elaborate retry/fallback logic for JSON formatting — a simple retry-once is sufficient.

**6. Consider scenario-aware strategy selection.** The performance gap between strategies varies by bug class (0.22 points for silent corruption vs. 0.83 for intermittent bugs). A future enhancement could classify the incoming bug report and select a strategy adapter accordingly, though a single hypothesis-driven adapter (omo-prometheus or omc-deep-interview) is a reasonable starting point.

## Extending the experiment

### Adding a new strategy

1. Create `adapters/my-strategy.md` following the normalized template (see existing adapters).
2. Add the name to the `STRATEGIES` array in `run-experiment.sh`.

### Adding a new scenario

1. Create `scenarios/my-scenario.json` with `title`, `body`, `quality`, `ground_truth` (including `acceptable_paths`), `expected_triage_extracts`, and `reporter_profile`.
2. Add the name to the `SCENARIOS` array in `run-experiment.sh`.

## File index

| File | Purpose |
|------|---------|
| `adapters/*.md` | Strategy adapters (normalized, ~40-50 lines each) |
| `scenarios/*.json` | Bug report scenarios with ground truth and reporter profiles |
| `prompts/triage-system.md` | Triage agent system prompt (revised resolve/ask framing) |
| `prompts/reporter-system.md` | Reporter agent system prompt (with realism profiles) |
| `prompts/judge-system.md` | Judge agent scoring rubric (revised, split efficiency) |
| `prompts/scenario-analysis-system.md` | Cross-strategy analysis prompt |
| `scripts/run-experiment.sh` | Main orchestrator |
| `scripts/run-single-trial.sh` | Single scenario x strategy dialogue loop |
| `scripts/judge.sh` | Judge agent invocation |
| `scripts/analyze-scenario.sh` | Cross-strategy scenario analysis |
| `scripts/summarize.sh` | Summary table generator (multi-trial aggregation) |
