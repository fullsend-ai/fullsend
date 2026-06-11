# Adaptive Agent Selection

## Problem

Fullsend currently treats agent configurations, team compositions, and workflow shapes as static choices — authored once by a human and not re-evaluated against outcomes. As the number of available agents, teams, and workflow patterns grows, the space of possible configurations becomes too large for manual tuning.

**How should fullsend learn, from its own execution history, which agent/team/workflow configurations are effective for which classes of problems — and adapt its selections accordingly?**

This sits at the intersection of two existing problem areas: [testing-agents.md](testing-agents.md) (CI for prompts, regression testing, eval frameworks) and [production-feedback.md](production-feedback.md) (platform execution signals feeding back into agent behavior). Adaptive selection is a concrete mechanism for turning execution outcomes into improvement signal, rather than treating configuration choices as static.

## Why this matters

Without a feedback loop, every configuration choice ossifies. As soon as fullsend has any runtime selection between alternative agents, teams, or workflow shapes — even a hand-coded conditional — a bad default persists silently unless something measures its outcomes against alternatives. Deciding up front how that measurement works is cheaper than retrofitting it once defaults are entrenched.

Fitness must be scoped by context. A team that is effective at one class of task (e.g., Python refactors) may be mediocre at another (e.g., GitHub Actions YAML edits or dependency bumps). If the platform ever supports more than one team or workflow, fitness data must be bucketed by context tuple (repo shape, language, issue type, phase, ...) — and that bucketing is much easier to design in than to bolt on.

For supply-chain-sensitive paths, there is an additional benefit: when configurations are chosen by a measured, data-backed policy rather than "the author picked it," there is a defensible audit trail for why a given team or configuration ran against a given change.

## Optimization surfaces

There are three distinct surfaces where adaptive selection could apply, each with different search-space characteristics:

### Team selection

Given a set of available agent teams, select the one most likely to succeed for a given task context. The search space is discrete and finite: pick one of N known teams. This is the cheapest surface to evaluate and the most natural starting point for an experiment.

### Agent configuration

Within a team, individual agent parameters (model choice, temperature, token budget, system prompt variants) form a structured configuration space. Optimization here requires more runs per configuration to distinguish signal from noise, but the per-run cost is the same as normal operation.

### Workflow shape

The sequence and composition of phases in a workflow (triage → implement → review → merge, or triage → implement → test → implement → review → merge, etc.) is a structured, ordered space. Crossover and mutation operations have natural interpretations: swapping phases, inserting a new phase, removing a redundant one.

### Token cost optimization

Agent runs have measurable token costs. A configuration that produces the same quality outcome with fewer tokens is strictly better. Cost optimization can be a secondary objective layered on top of quality-focused selection, or a tiebreaker between configurations with similar fitness.

## Candidate approaches

### Thompson Sampling

Well-suited for discrete selection problems (pick one of N teams or configurations). Each option maintains a Beta distribution parameterized by successes and failures. Selection samples from each distribution and picks the option with the highest sample — naturally balancing exploration (uncertain options get sampled broadly) and exploitation (high-performing options get sampled near their mean).

Strengths:
- Clean Bayesian interpretation
- Naturally handles exploration vs. exploitation without tuning an epsilon parameter
- Low computational overhead
- Works well with sparse, noisy reward signals

Weaknesses:
- Assumes the set of options is fixed and enumerable
- Does not capture structure in the configuration space (two similar configurations are treated as independent)
- Requires defining success/failure in binary terms (or extending to continuous rewards via conjugate priors)

### Genetic algorithms

Well-suited when the search space is structured and operations like crossover and mutation have natural interpretations. A workflow represented as an ordered list of phases is a natural genome: crossover swaps sub-sequences between two workflows, mutation inserts, removes, or replaces a phase.

Strengths:
- Can explore structured, high-dimensional spaces
- Crossover can combine good sub-components from different configurations
- Does not require gradient information

Weaknesses:
- Requires a population, which means many parallel evaluations per generation
- Convergence is slow relative to Bayesian methods for small, discrete spaces
- Fitness evaluation is expensive (each genome requires one or more real agent runs)
- Designing meaningful crossover and mutation operators requires domain knowledge

### Hybrid approach

A plausible split: Thompson Sampling for team and configuration selection (discrete, bounded), genetic algorithms for workflow evolution (structured, ordered). This avoids using GA where a simpler method works and avoids using TS where the space has exploitable structure.

## Fitness function design

The fitness function is the hardest design problem. It must be:

1. **Honest** — not gameable by agents (an agent that writes trivial tests to inflate pass rate should not score well)
2. **Multi-dimensional** — no single metric captures quality
3. **Normalized by difficulty** — a policy should not learn to prefer easy tasks that happen to succeed more often

### Candidate signals

| Signal | Strengths | Gameability risk |
|--------|-----------|-----------------|
| Build/test pass rate | Easy to measure, immediate | High — agents can write trivial tests |
| PR merge rate | Reflects human judgment | Medium — merge criteria vary by repo |
| Reviewer change count | Measures first-attempt quality | Low — hard to game without reviewer collusion |
| Time-to-green | Measures efficiency | Medium — can be gamed by reducing test scope |
| Regression rate (next N runs) | Measures durability | Low — but slow to measure |
| Human thumbs-up/down | Direct quality signal | Low — but sparse and expensive |
| Token cost per successful outcome | Measures efficiency | Low — but incentivizes shortcuts |

A composite fitness function combining several signals with configurable weights is likely necessary. The weights themselves are a design decision that affects what the system optimizes for — and the weights should be auditable and human-adjustable, not learned.

### Difficulty normalization

Without normalization, the policy learns to prefer configurations that happen to be assigned easy tasks. Approaches:

- **Stratified evaluation**: ensure each configuration is tested across a representative distribution of task difficulties
- **Difficulty estimation**: classify tasks by proxy signals (lines changed, number of files, language complexity) and normalize fitness within difficulty bands
- **Head-to-head comparison**: when two configurations are close in fitness, run both on the same task and compare directly (A/B shadow testing)

## Context granularity

Fitness must be bucketed by context — but how fine-grained?

| Granularity | Example tuple | Risk |
|-------------|---------------|------|
| Too coarse | `(language)` | Python-expert teams get credit for JS wins |
| Reasonable | `(repo, language, issue_type)` | Balances specificity and data volume |
| Too fine | `(repo, file_path, issue_label, author)` | Every bucket is cold-start forever |

The right granularity is an empirical question. A hierarchical approach — start coarse, split when a bucket accumulates enough data to reveal within-bucket variance — avoids committing to a fixed schema up front.

### Separate genomes per domain

For GA-based workflow evolution, it may be worth maintaining separate populations per domain (security workflows, cost-optimization workflows, feature-development workflows). Cross-domain crossover is unlikely to produce useful offspring — the constraints and phase ordering differ too much.

## Cold start

New agents, new teams, new repos, and new context tuples will have no fitness data. Several strategies exist, and they are not mutually exclusive:

- **Similarity fallback**: Use fitness data from the nearest known context. For example, `python_django` falls back to `python_*`, which falls back to `generic`. Requires defining a similarity metric over context tuples.
- **Mandatory exploration budget**: Reserve a fraction of runs (ε-greedy floor) for untested configurations, ensuring new options are not starved. The exploration budget should decay as data accumulates.
- **Prior from defaults**: Initialize new configurations with the fitness distribution of the current hand-picked default, then update as real data arrives. This avoids the cold-start penalty of a uniform prior.
- **Transfer from similar repos**: If repo A and repo B share language, framework, and team composition, fitness data from A can bootstrap B. The transfer should decay over time as repo-specific data accumulates.

## Shadow mode

Before any learned recommendation drives a real run, it should be validated in shadow mode:

1. The adaptive policy proposes a configuration alongside the current default
2. Both run (or the default runs and the alternative is recorded as a counterfactual)
3. Outcomes are compared over N runs
4. The policy is promoted to live selection only when it demonstrates statistically significant improvement over the default

Shadow mode is the safest path to deployment and likely a prerequisite for any production use. It also generates the comparison data needed to validate the fitness function itself — if the policy's recommended configurations consistently score higher on the fitness function but produce worse human-judged outcomes, the fitness function is wrong.

## Retirement and recovery

Configurations that were once fit may decay after a model version change, a framework upgrade, or a shift in the task distribution. Hard deletion of fitness data is lossy — the configuration may become viable again.

Options:
- **Soft retirement with weight decay**: Multiply historical fitness by a decay factor (e.g., 0.95 per time period). Old successes contribute less; if the configuration stops winning, it naturally drops out of selection without losing its history.
- **Recovery probing**: Periodically re-test retired configurations at a low rate (part of the exploration budget). If performance has recovered, the configuration re-enters the active population.
- **Version-tagged fitness**: Tag fitness data with the model version, framework version, or other environmental metadata. When the environment changes, reset or discount data from the previous version.

## Safety constraints

### Deterministic safety reconciliation

Fullsend's principle of deterministic safety must be preserved. The adaptive selection layer chooses *which* configuration runs — it does not modify the safety checks *within* a run. Branch protection, CODEOWNERS, status checks, and review gates remain unchanged regardless of which team or workflow the policy selects.

The selection policy itself must be auditable: every selection decision should be logged with the context tuple, the candidate configurations, their fitness scores, the sampling state, and the chosen option. This log is the audit trail for why a given configuration ran.

### Failure modes to explicitly avoid

- **Self-deployment of learned changes**: The policy must not continuously deploy its own learned configurations without a human checkpoint. A human must approve the transition from shadow mode to live selection for each context bucket.
- **Self-referential fitness**: The policy must not be able to influence its own fitness signal. If the adaptive layer selects a team, that team's output must be evaluated by an independent process — not by a component that the adaptive layer also controls.
- **Optimization pressure on safety paths**: Configurations that skip or weaken safety checks (fewer review agents, relaxed merge criteria) will score better on speed metrics. The fitness function must not reward configurations that reduce safety coverage. Safety checks should be constraints, not objectives — a configuration that violates safety constraints is infeasible regardless of its fitness score.

## Storage and observability

Fitness state needs a persistent store and a human-readable surface:

- **Storage**: A lightweight relational store (SQLite or equivalent) keyed by context tuple, with columns for configuration ID, success/failure counts, fitness scores, timestamps, and environmental metadata.
- **Query surface**: A CLI command (`fullsend fitness show --context <tuple>`) or dashboard endpoint that allows humans to inspect what the policy has learned, which configurations are favored, and whether the learning looks reasonable.
- **Alerting**: If the policy's selections diverge sharply from historical defaults (e.g., it stops selecting a previously dominant team), that divergence should surface as a notification for human review.

## Experiment design

A minimum viable experiment would:

1. **Pick one optimization surface**: Team selection is the cheapest to evaluate.
2. **Define a trusted fitness function**: Start with a composite of PR merge rate and reviewer-change-count, normalized by task difficulty proxy.
3. **Implement shadow mode**: The learned policy proposes a team alongside the current default without replacing it.
4. **Run for N tasks**: Accumulate comparison data over a meaningful sample size (likely 50-100 runs per context bucket).
5. **Evaluate**: Does the policy converge? Does it beat the default? Does the fitness function agree with human judgment?

The experiment should actively pressure-test three assumptions:
- (a) the fitness function is honest and not gameable by agents
- (b) the context bucketing is fine-grained enough to be meaningful but coarse enough to accumulate data
- (c) cold-start and exploration are handled so new or unused configurations are not starved

## Relationship to other problems

**[Testing the Agents](testing-agents.md)**: Adaptive selection generates exactly the kind of outcome data an agent-eval framework needs — per-configuration, per-context performance metrics. The fitness data from adaptive selection could feed directly into agent regression testing.

**[Production Feedback](production-feedback.md)**: Adaptive selection is one concrete mechanism for closing the production feedback loop. Instead of production signals only triggering reactive fixes, they contribute to the fitness function that guides proactive configuration selection.

**[Operational Observability](operational-observability.md)**: The selection policy's decisions, fitness scores, and convergence behavior are a new dimension of operational state that operators need to understand. The observability tools described there need to accommodate adaptive selection's audit trail.

**[Agent Architecture](agent-architecture.md)**: The adaptive layer sits above the agent architecture — it selects which agents and teams run, but does not modify the agents themselves. The boundary between "adaptive selection" and "agent self-modification" must be clearly maintained.

**[Governance](governance.md)**: Who controls the fitness function weights? Who approves the transition from shadow to live mode? Who can override the policy's selection? These are governance questions that the adaptive selection layer must integrate with.

## Open questions

- What is the right composite fitness function, and who sets the weights? Should weights be fixed per organization, per repo, or learned?
- What is the minimum sample size per context bucket before the policy's recommendations are statistically meaningful?
- GA vs. Thompson Sampling — or both at different layers? TS for team/config selection, GA for workflow evolution is the leading hypothesis, but it needs validation.
- How fine-grained should context tuples be? A hierarchical split-on-variance approach avoids committing to a fixed schema, but adds implementation complexity.
- How do we handle model version changes — reset fitness data, apply a discount, or version-tag and keep history?
- Should the fitness function include a cost term from the start, or should cost optimization be a separate experiment after quality optimization is validated?
- What is the right exploration budget, and how should it decay? Too high wastes runs on poor configurations; too low starves new options.
- Can fitness data transfer across repos with similar characteristics, and how do we measure "similar"?
- How should the adaptive layer interact with the deterministic safety model — is selection-only sufficient, or are there edge cases where the choice of configuration implicitly affects safety coverage?
- What prevents a well-scoring configuration from being well-scoring only because it is assigned to a biased subset of tasks (selection bias in the evaluation)?
- Should there be a human-in-the-loop approval step before any configuration is retired, or is soft retirement with recovery probing sufficient?
