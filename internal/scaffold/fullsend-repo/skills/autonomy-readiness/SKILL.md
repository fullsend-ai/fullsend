---
name: autonomy-readiness
description: >
  Use when you need to analyze the delta between agent review and human review
  on a PR to identify structural repo improvements that would close review gaps
  or justify increased agent autonomy.
---

# Autonomy Readiness Analysis

Analyze the difference between what the review agent found on a PR and what human reviewers found on the same PR. Use those deltas to propose concrete changes that either close gaps or, when agent coverage is strong, justify relaxing human oversight. You need the full PR timeline -- agent findings, human comments, requested changes, and inline suggestions -- before you can extract a meaningful delta.

## Phase 1: Extract the delta

Build two sets from the PR timeline:

**Agent findings** -- for each finding, record:
- Severity (as the agent expressed it)
- Category (e.g., correctness, style, security, testing, documentation)
- File and location
- Description of the issue raised

**Human findings** -- for each piece of human feedback, record:
- The comment, requested change, or inline suggestion
- The file and location (if applicable)
- The substance of the concern

**Classify each human finding:**

- **Matched** -- the agent raised a finding of similar substance and severity. Wording differences do not matter; what matters is whether the agent identified the same underlying problem at a comparable severity level.
- **Gap** -- the agent missed the issue entirely, or raised it at a significantly lower severity than the human reviewer judged appropriate.

**Classify each agent finding with no human counterpart:**

- **Novel** -- the agent raised something no human commented on. This is not a gap, but it is worth noting: a pattern of novel findings with no human agreement may signal false positives.

## Phase 2: Diagnose root causes (for gaps)

For each gap, work through the following diagnostic checklist. Stop at the first category that fits:

1. **Missing context** -- the human had domain knowledge the agent could not access: downstream consumers, production behavior, undocumented team conventions, deployment topology. Repo change: document the missing context so that future reviews (agent or human) can reference it.

2. **Missing test coverage** -- a test would have let the agent flag inadequate testing or catch a behavioral regression. Repo change: add or improve tests that cover the gap.

3. **Missing CI gate** -- a linter rule, static analysis check, or CI validation would catch this class of issue deterministically. Repo change: add the specific rule or check.

4. **Missing skill or prompt guidance** -- the review agent lacks guidance for this class of issue. Note this as an upstream improvement opportunity, but focus proposals on what the repo itself can do. Documentation and tests often compensate for missing agent guidance.

5. **Insufficient repo documentation** -- conventions, constraints, or architectural decisions are not written down. Repo change: write the missing documentation (ADRs, AGENTS.md updates, inline comments, README sections).

## Phase 3: Assess successes

When agent findings fully cover human review (all findings matched, no gaps), characterize the success:

- Paths touched in the PR
- Change type (bug fix, feature, refactor, docs, config)
- Complexity (lines changed, files touched, cross-cutting vs. localized)
- Agent outcome (approved, requested changes that human agreed with)

Look for patterns across multiple PRs. A single success is an anecdote, not a pattern. Three or more similar PRs where the agent fully covered human review is a signal worth acting on.

When a pattern emerges, identify what autonomy mechanism could be relaxed:
- CODEOWNERS entries (removing human reviewers for specific paths)
- Protected path rules
- Agent permissions or team membership
- Auto-merge scope (allowing agent-approved PRs to merge without human sign-off for specific change types)

## Proposal framing

### Gap-closing proposals

For each gap-closing proposal, include:

- **Identified gap:** what the human caught, what the agent missed, and a link to the PR where it occurred.
- **Root cause:** which diagnostic category from Phase 2 applies and why.
- **Proposed repo change:** the specific file, config, test, or documentation change. Be concrete enough for an implementer to act on.
- **Validation criteria:** how to verify the gap is closed. Define a measurable or observable outcome with a timeframe or sample size.

### Autonomy-increasing proposals

For each autonomy-increasing proposal, include:

- **Evidence:** which PRs demonstrate the pattern, what class of change they represent, and how agent and human review compared.
- **Proposed change:** the specific mechanism to relax and the exact scope (e.g., "remove `@backend-team` from CODEOWNERS for `internal/utils/`", not "give the agent more autonomy").
- **Experiment:** how to trial the change safely. Shadow mode before real autonomy. Scoped trial before broad rollout. Gradual expansion with checkpoints.
- **Rollback criteria:** the conditions under which the change should be reverted. Be specific.

### Conservatism principle

When in doubt, prefer the smaller change. One directory before an entire subtree. Shadow mode before real autonomy. Every proposal must be individually reversible. If you cannot define rollback criteria for a proposal, the proposal is too aggressive -- narrow the scope until rollback is straightforward.

## What to propose

Think broadly about what would make a difference. Common categories include tests, CI gates, documentation, CODEOWNERS changes, protected path rules, agent permissions, and auto-merge scope -- but do not limit yourself to these. If you identify a novel change that would close a gap or justify more autonomy, propose it.

In particular, consider adding new agent skills to the target repo's `.claude/` directory. Skills added there are automatically picked up by both the fullsend review agent and casual Claude Code users. If a human reviewer consistently catches a class of issue that the agent misses, a repo-level skill teaching that pattern may be more effective than any other single change.
