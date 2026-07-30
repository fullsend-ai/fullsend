---
title: "78. PR-level risk assessment scoring"
status: Accepted
relates_to:
  - code-review
  - agent-architecture
topics:
  - review
  - risk
  - sub-agents
  - scoring
---

# 78. PR-level risk assessment scoring

Date: 2026-07-30

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

The review pipeline has no quantitative risk signal. Protected-path checks in
`post-review.sh` provide a binary gate, and the orchestrator's scope
classification (trivial/small/standard) captures size but not risk. There is no
composite score that accounts for path sensitivity, git history churn, author
context, or linked-issue complexity — signals that would inform review effort,
model selection, and auto-merge eligibility.

The prioritize agent's RICE scoring
([agents/agents/prioritize.md](https://github.com/fullsend-ai/agents)) provides
a proven pattern: agent produces structured JSON → post-script applies labels
and posts a breakdown comment.

A key constraint is that the fullsend harness expands `env.sandbox` values
before the pre-script runs ([#5756](https://github.com/fullsend-ai/fullsend/issues/5756)),
so pre-script-computed values cannot flow into the sandbox. Sub-agents currently
share the parent sandbox ([#3978](https://github.com/fullsend-ai/fullsend/issues/3978));
a future sub-agent harness schema ([#3982](https://github.com/fullsend-ai/fullsend/issues/3982))
may enable native skill loading per sub-agent.

## Options

### Option A: Sub-agent only (all tiers in LLM)

A risk-assessment sub-agent in the pr-review orchestrator computes all signals
via LLM reasoning. No bash script for metadata extraction.

**Rejected.** Deterministic metadata signals (file counts, path matches,
dependency file detection) are cheaper and more reliable via bash. Sending
trivially computable signals through the LLM wastes tokens and introduces
non-determinism.

### Option B: Pre-script + sub-agent

`pre-review.sh` computes metadata tier signals in bash, passes them to the
sandbox via env vars, and a sub-agent handles git-history and linked-issue tiers.

**Rejected.** Blocked by [#5756](https://github.com/fullsend-ai/fullsend/issues/5756) —
pre-script env vars do not propagate to the sandbox.

### Option C: Standalone agent (separate harness stage)

A dedicated `risk-assessment` harness stage runs before the review stage.

**Rejected.** Adds pipeline latency (sequential stage) and orchestration
complexity (inter-stage result passing) for a signal that integrates naturally
into the review pipeline.

## Decision

Add PR-level risk assessment as a **pre-pass sub-agent inside the review
pipeline** (Option B revised — metadata extraction moved inside the sandbox to
work around [#5756](https://github.com/fullsend-ai/fullsend/issues/5756)).

**Components:**

- `skills/pr-risk-assessment/SKILL.md` — scoring model, signal tier definitions,
  anchoring examples, output format.
- `skills/pr-risk-assessment/scripts/risk-tier1.sh` — deterministic metadata
  signals (blast radius, path sensitivity, CI/workflow impact, dependency risk,
  test coverage ratio, author context). Run by the sub-agent via Bash inside the
  sandbox.
- `skills/pr-review/sub-agents/risk-assessment.md` — sub-agent definition,
  dispatched as a pre-pass on every PR (unlike `security-triage` which only runs
  on large PRs). Model: sonnet.

**Skill loading** follows the docs-currency convention: the orchestrator reads
both the sub-agent `.md` and `skills/pr-risk-assessment/SKILL.md`, concatenates
them into the prompt. When sub-agents gain isolated sandboxes
([#3978](https://github.com/fullsend-ai/fullsend/issues/3978),
[#3982](https://github.com/fullsend-ai/fullsend/issues/3982)), the sub-agent
could load the skill natively.

**Scoring model:** Three signal tiers with weighted sub-scores (1–5 each):

| Signal tier | Weight | Signals |
|---|---|---|
| Metadata (bash, deterministic) | 50% | Blast radius, path sensitivity, CI/workflow, dependencies, test coverage, author context |
| Git history (LLM-assisted) | 30% | Churn hotspots, multi-author contention, regression history, change coupling, code age, revert frequency |
| Linked issue context (LLM-assisted) | 20% | Complexity/scope mismatch, issue labels, acceptance criteria, discussion history, staleness |

When no linked issue exists, tier weights redistribute proportionally (metadata
62%, git history 38%). The composite is a weighted average rounded to the nearest
integer.

**Output:** An optional `risk_assessment` object in the review result JSON
(absent when the feature flag is off):

```json
{
  "score": 3,
  "level": "elevated",
  "tier1_signals": [{"dimension": "...", "value": "..."}],
  "tier2_signals": [...],
  "tier3_signals": [...],
  "rationale": "..."
}
```

`score` (1–5), `level` (enum: low/moderate/elevated/high/critical), and
`rationale` are required within the object. Signal arrays are optional for
graceful degradation.

**Post-review integration:** `post-review.sh` reads the risk assessment from
the result JSON, applies a `risk/*` label (removing any prior `risk/*` label),
and appends a breakdown table to the PR comment. Risk level is informational
only — it does not gate the review outcome. The protected-path check remains
the sole blocking mechanism.

**Feature flag:** `FULLSEND_RISK_ASSESSMENT_ENABLED` env var, default `true`.

## Consequences

- Review pipeline gains a quantitative risk signal visible via labels and PR
  comments, enabling risk-informed triage and review prioritization.
- Risk scoring adds one sonnet-model sub-agent call per PR (cost-effective
  relative to the opus dimension sub-agents).
- Metadata tier signals are deterministic and auditable via bash script output.
- Risk level is decoupled from review outcome — gating can be added later once
  scoring confidence is established.
- The docs-currency skill-loading pattern adds an implicit coupling between the
  orchestrator and the skill directory; this coupling is eliminated when sub-agent
  sandbox isolation lands ([#3978](https://github.com/fullsend-ai/fullsend/issues/3978)).

## References

- [#4698](https://github.com/fullsend-ai/fullsend/issues/4698) — PR-level risk assessment scoring
- [#5756](https://github.com/fullsend-ai/fullsend/issues/5756) — Pre-script → sandbox data flow
- [#3978](https://github.com/fullsend-ai/fullsend/issues/3978) — Sub-agent sandbox isolation
- [#3982](https://github.com/fullsend-ai/fullsend/issues/3982) — Sub-agent harness schema ADR
- [ADR 0045](0045-forge-portable-harness-schema.md) — Forge-portable harness schema
