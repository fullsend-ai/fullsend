# Autonomy Readiness Skill Design

**Date:** 2026-06-22
**Status:** Draft

## Purpose

A new skill that provides a structured methodology for analyzing the delta between agent review and human review on a PR. It identifies structural repo improvements that would close review gaps or justify increased agent autonomy.

The skill is agent-agnostic. Any agent (or human) with access to a PR timeline can use it. The initial consumer is the retro agent, but the retro-specific framing lives in the retro agent's prompt, not in the skill.

## Description (for skill frontmatter)

"Use when you need to analyze the delta between agent review and human review on a PR to identify structural repo improvements that would close review gaps or justify increased agent autonomy."

## What It Produces

Zero or more proposals that either:

- **Close a gap** between human and agent review by diagnosing a structural root cause in the repo and proposing a specific change.
- **Increase autonomy** by identifying a pattern of agent review success and proposing a specific, conservative expansion of agent authority.

Proposals compete for whatever budget the consuming agent has. The skill does not define a budget; it defines a methodology.

## Analysis Methodology

### Phase 1: Extract the Delta

For a given PR, build two sets:

- **Agent findings** — the review agent's posted findings (severity, category, file, description).
- **Human findings** — every piece of human review feedback (comments, requested changes, inline suggestions).

Classify each human finding:

- **Matched** — the agent raised a finding of similar substance and severity. Wording differences do not matter.
- **Gap** — the agent missed it entirely, or raised it at significantly lower severity.

Classify each agent finding:

- **Novel** — the agent raised something the humans did not. Not a gap, but worth noting as potential false-positive signal.

### Phase 2: Diagnose Root Causes (for gaps)

For each gap, ask: why did the human catch this and the agent did not? Apply the following diagnostic checklist:

1. **Missing context** — the human had domain knowledge the agent could not access (knowledge of downstream consumers, production behavior, team conventions not documented anywhere).
2. **Missing test coverage** — a test for the behavior in question would have let the review agent flag the change as inadequately tested.
3. **Missing CI gate** — a linter rule, static analysis check, or CI validation would have caught this deterministically.
4. **Missing skill or prompt guidance** — the review agent lacks guidance for this class of issue. This is an upstream improvement to fullsend; note it but it is not the primary focus of this skill.
5. **Insufficient repo documentation** — conventions, architectural decisions, or constraints are undocumented, forcing the human to rely on tribal knowledge.

### Phase 3: Assess Successes (for matched findings)

When the agent's findings fully cover the human review (all human findings are matched, no gaps):

- Note the PR characteristics: what paths were touched, what kind of change, how complex.
- Look for patterns: has this class of change been reliably reviewed by the agent across multiple PRs?
- Identify what specific autonomy mechanism could be relaxed for this class of change.

## Proposal Framing

### Gap-Closing Proposals

Each proposal identifies:

- **The gap** — what the human caught, what the agent missed, on which PR.
- **The root cause** — which diagnostic category from Phase 2, with reasoning.
- **The proposed repo change** — one of:
  - Add or improve a test that would make the gap detectable.
  - Add a CI gate or linting rule that catches it deterministically.
  - Document a convention or constraint so the agent has access to it.
  - Improve CODEOWNERS coverage so a domain expert is required on that path.
- **Validation criteria** — how to verify the change actually closes the gap (e.g., re-run review agent on the original PR diff and confirm it now raises the finding).

### Autonomy-Increasing Proposals

Each proposal identifies:

- **The evidence** — which PRs, what class of change, how agent findings compared to human findings.
- **The proposed change** — one of:
  - Relax CODEOWNERS for a specific path.
  - Remove a path from the protected paths list.
  - Grant the review agent additional repo permissions or team membership.
  - Enable auto-merge for a narrowly scoped class of PR.
- **The experiment** — a conservative trial before full enactment (e.g., run in shadow mode for N PRs, or apply to test-only changes for 2 weeks before expanding scope).
- **Rollback criteria** — what would trigger reverting the change (e.g., if a human overrides agent review on a PR in this scope within the trial period).

### Conservatism Principle

When in doubt, propose the smaller change. Relax CODEOWNERS for one directory before proposing it for a whole subtree. Propose shadow mode before real autonomy. Every proposal must be individually reversible.

## Scope of Repo-Level Changes

The following are in scope for proposals:

- Tests (unit, integration, end-to-end)
- CI gates and linting rules
- Documentation of conventions and constraints
- CODEOWNERS modifications
- Protected paths list modifications
- Agent permissions and team membership in the repo
- Auto-merge scope configuration

The following are out of scope (note them, but do not propose them as the primary action):

- Upstream changes to fullsend skills, prompts, or sub-agents
- Changes to the review agent's model or architecture

## Integration with Retro Agent

The skill is agent-agnostic. Retro-specific integration requires:

### `agents/retro.md`

Add a section instructing the retro agent to invoke the `autonomy-readiness` skill after reconstructing the PR timeline. Proposals from this analysis compete for the standard 3-proposal budget alongside other improvement proposals.

### `harness/retro.yaml`

Add `autonomy-readiness` to the skills list alongside `retro-analysis`, `finding-agent-runs`, and `agent-scaffolding`.

### No other changes

No schema changes (proposals use existing format: `target_repo`, `title`, `what_happened`, `what_could_go_better`, `proposed_change`, `validation_criteria`). No workflow changes (same trigger, same output mechanism, same proposal cap).

Field mapping:

- `what_happened` — the delta analysis (what agent caught, what human caught).
- `what_could_go_better` — the root cause diagnosis.
- `proposed_change` — the specific repo change.
- `validation_criteria` — the experiment and rollback criteria.

## Skill File Structure

`internal/scaffold/fullsend-repo/skills/autonomy-readiness/SKILL.md` containing:

1. Frontmatter (name, description)
2. Overview
3. Phase 1: Extract the delta
4. Phase 2: Diagnose root causes
5. Phase 3: Assess successes
6. Proposal framing (gap-closing and autonomy-increasing templates)
7. What's in scope for proposals
8. What's out of scope
