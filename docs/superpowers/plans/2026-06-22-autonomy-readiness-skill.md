# Autonomy Readiness Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create an agent-agnostic skill for analyzing the delta between agent review and human review, and integrate it with the retro agent.

**Architecture:** A single SKILL.md file containing the full methodology (phases 1-3 and proposal templates), a one-line addition to the retro harness, and a short addition to the retro agent prompt. Existing scaffold tests validate the wiring automatically.

**Tech Stack:** Markdown (skill definition), YAML (harness config), Go (existing scaffold tests)

**Spec:** `docs/superpowers/specs/2026-06-22-autonomy-readiness-skill-design.md`

---

### Task 1: Create the autonomy-readiness skill directory and SKILL.md

**Files:**
- Create: `internal/scaffold/fullsend-repo/skills/autonomy-readiness/SKILL.md`

- [ ] **Step 1: Create the skill directory**

```bash
mkdir -p internal/scaffold/fullsend-repo/skills/autonomy-readiness
```

- [ ] **Step 2: Write the SKILL.md file**

Create `internal/scaffold/fullsend-repo/skills/autonomy-readiness/SKILL.md` with the following content:

```markdown
---
name: autonomy-readiness
description: >
  Use when you need to analyze the delta between agent review and human review
  on a PR to identify structural repo improvements that would close review gaps
  or justify increased agent autonomy.
---

# Autonomy Readiness Analysis

Analyze what humans caught that agents missed (and vice versa) on a PR review.
Diagnose structural root causes in the repo and propose changes that close
gaps or justify increased agent autonomy.

## When to use this skill

Use after you have access to:
- The review agent's posted findings on a PR (severity, category, file, description).
- The human review feedback on the same PR (comments, requested changes, inline suggestions).

## Phase 1: Extract the delta

Build two sets from the PR timeline:

**Agent findings** — each finding the review agent posted, with its severity, category, file, and description.

**Human findings** — each piece of human review feedback: inline comments, review-level comments, and requested changes.

Classify each human finding:

- **Matched** — the agent raised a finding of similar substance and severity. Wording differences do not matter. If the agent said "this nil check is missing" and the human said "what happens when this is null?", that is a match.
- **Gap** — the agent missed it entirely, or raised it at meaningfully lower severity (e.g., agent said `info`, human flagged it as a blocking concern).

Classify each agent finding that has no human counterpart:

- **Novel** — the agent raised something the humans did not. This is not a gap, but note it. A pattern of unmatched novel findings may indicate false positives worth investigating.

## Phase 2: Diagnose root causes (for gaps)

For each gap, ask: why did the human catch this and the agent did not?

Work through this diagnostic checklist in order. Stop at the first category that fits — most gaps have one primary root cause.

### 1. Missing context

The human had domain knowledge the agent could not access. Examples:
- Knowledge of downstream consumers or production behavior not documented in the repo.
- Team conventions that exist only as tribal knowledge.
- History of past incidents related to this code path.

**Repo change:** Document the missing context. Add it to a CONTRIBUTING.md, architecture doc, or inline code comment so the agent can access it on future reviews.

### 2. Missing test coverage

A test for the behavior in question would have let the review agent flag the change as inadequately tested. Examples:
- Human pointed out an edge case — if a test existed for that edge case, the review agent could have noticed the change did not update the test.
- Human flagged a regression risk — if regression tests existed, the review agent could have flagged missing coverage.

**Repo change:** Add or improve tests that make the gap detectable. Specify which test, what it should assert, and why its absence prevented the agent from catching the issue.

### 3. Missing CI gate

A linter rule, static analysis check, or CI validation would have caught this deterministically. Examples:
- Human flagged a naming convention violation — a linter rule would catch this without LLM involvement.
- Human caught a security anti-pattern — a static analysis rule would flag it.

**Repo change:** Add the specific linter rule, static analysis check, or CI gate. Name the tool and rule ID if applicable.

### 4. Missing skill or prompt guidance

The review agent lacks guidance for this class of issue. The agent's skills and sub-agent prompts do not cover the pattern the human recognized. Examples:
- Human caught an API contract violation that the review agent's correctness sub-agent is not trained to look for.
- Human applied a repo-specific architectural principle the agent has no access to.

**Repo change:** This is primarily an upstream fullsend improvement (new skill content or sub-agent prompt refinement). Note it, but focus your proposal on what the *repo* can do — often a combination of documentation (category 1) and tests (category 2) can compensate.

### 5. Insufficient repo documentation

Conventions, architectural decisions, or constraints are not written down anywhere in the repo. The human relied on experience that is not encoded. Examples:
- "We never use pattern X in this codebase because of Y" — not documented.
- Architectural decision records that would explain why a certain approach is wrong — not written.

**Repo change:** Write the missing documentation. ADRs, CONTRIBUTING.md sections, or README updates that encode the knowledge the human used.

## Phase 3: Assess successes

When the agent's findings fully cover the human review — all human findings are matched, no gaps — this PR is evidence that the agent could have handled this review with more autonomy.

Characterize the success:
- **Paths touched** — which directories and file types were in the PR.
- **Change type** — bug fix, feature, refactor, docs, tests, config.
- **Complexity** — number of files, lines changed, number of review findings.
- **Agent outcome** — what action did the agent take (approve, request-changes, comment).

Look for patterns: has this class of change been reliably reviewed by the agent across multiple PRs? A single success is not a pattern. If you can identify 3+ PRs where the agent matched or exceeded human review for similar changes, that is a signal worth proposing on.

Identify what specific autonomy mechanism could be relaxed:
- CODEOWNERS for the affected paths.
- Protected paths list.
- Agent permissions or team membership in the repo.
- Auto-merge eligibility for this class of change.

## Proposal framing

### Gap-closing proposals

Each proposal identifies:

- **The gap** — what the human caught, what the agent missed, on which PR (link to the specific comment).
- **The root cause** — which diagnostic category, with reasoning about why you chose it.
- **The proposed repo change** — the specific file, config, or documentation to add or modify.
- **Validation criteria** — how to verify the change closes the gap. Prefer concrete checks: "re-run the review agent on the original PR diff and confirm it raises a finding about X" or "the next PR that touches this path should trigger CI gate Y."

### Autonomy-increasing proposals

Each proposal identifies:

- **The evidence** — which PRs, what class of change, how agent findings compared to human findings. Link to each PR.
- **The proposed change** — the specific autonomy mechanism to relax and the exact scope (one directory, one file pattern, one change type).
- **The experiment** — a conservative trial before full enactment. Examples:
  - Shadow mode: apply the change but require a human to verify the agent's decision for N PRs before removing the human gate.
  - Scoped trial: apply only to the narrowest possible scope (one subdirectory, test-only changes) for a defined period.
  - Gradual expansion: start with the least-risky subset and expand if no regressions occur.
- **Rollback criteria** — what triggers reverting the change. Be specific: "if a human overrides the agent's review decision on any PR in this scope during the trial period, revert the change and investigate."

### Conservatism principle

When in doubt, propose the smaller change. One directory before a subtree. Shadow mode before real autonomy. Every proposal must be individually reversible. If you cannot define a rollback criterion, the proposal is too aggressive.

## In-scope repo changes

- Tests (unit, integration, end-to-end).
- CI gates and linting rules.
- Documentation (CONTRIBUTING.md, ADRs, architecture docs, inline comments).
- CODEOWNERS modifications.
- Protected paths list modifications.
- Agent permissions and team membership in the repo.
- Auto-merge scope configuration.

## Out of scope

- Upstream changes to fullsend skills, prompts, or sub-agents. Note these if relevant, but do not propose them as the primary action.
- Changes to the review agent's model or architecture.
```

- [ ] **Step 3: Verify the skill frontmatter parses correctly**

Run: `go test ./internal/skill/ -run TestParseFrontmatter -v`
Expected: PASS (existing frontmatter parsing tests confirm the parser works; this validates the parser is not broken, not the new file specifically)

- [ ] **Step 4: Commit**

```bash
git add internal/scaffold/fullsend-repo/skills/autonomy-readiness/SKILL.md
git commit -S -s -m "feat: add autonomy-readiness skill

Agent-agnostic methodology for analyzing the delta between agent review
and human review on a PR. Identifies structural repo improvements that
close review gaps or justify increased agent autonomy.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

### Task 2: Wire the skill into the retro harness

**Files:**
- Modify: `internal/scaffold/fullsend-repo/harness/retro.yaml:24-27` (skills list)

- [ ] **Step 1: Add autonomy-readiness to the retro harness skills list**

In `internal/scaffold/fullsend-repo/harness/retro.yaml`, add `skills/autonomy-readiness` to the `skills` array. The current skills list is:

```yaml
skills:
  - skills/retro-analysis
  - skills/finding-agent-runs
  - skills/agent-scaffolding
```

Change it to:

```yaml
skills:
  - skills/retro-analysis
  - skills/finding-agent-runs
  - skills/agent-scaffolding
  - skills/autonomy-readiness
```

- [ ] **Step 2: Run scaffold validation tests**

Run: `go test ./internal/scaffold/ -run TestHarnessesLoadAndValidate -v`
Expected: PASS — this test extracts the scaffold to a temp dir, loads all harness YAMLs, and calls `ValidateFilesExist()` which confirms that every skill directory in the `skills` array exists. This validates the wiring end-to-end.

- [ ] **Step 3: Run the full scaffold test suite to check for regressions**

Run: `go test ./internal/scaffold/ -v`
Expected: PASS — all existing tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/scaffold/fullsend-repo/harness/retro.yaml
git commit -S -s -m "feat(retro): wire autonomy-readiness skill into harness

Adds the autonomy-readiness skill to retro's skill list so it is
available during retrospective analysis.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

### Task 3: Add autonomy-readiness guidance to the retro agent prompt

**Files:**
- Modify: `internal/scaffold/fullsend-repo/agents/retro.md:38-44` (optimization goals section)

- [ ] **Step 1: Add autonomy readiness as an optimization goal**

In `internal/scaffold/fullsend-repo/agents/retro.md`, the current optimization goals are:

```markdown
## Optimization goals

Evaluate workflows through these lenses (in priority order):

1. **Review quality** — Are reviews catching real issues? Are they missing things? Are they flagging false positives that waste human time?
2. **Rework rate** — How many iterations did it take? Could the code agent have gotten it right the first time with better context or instructions?
3. **Token cost** — Are agents doing redundant work? Reading files they don't need? Exploring dead ends?
4. **Time to resolution** — Could the pipeline have moved faster without sacrificing quality?
```

Add a fifth goal after the existing four:

```markdown
5. **Autonomy readiness** — What did human reviewers catch that the review agent missed? What repo-level changes would close those gaps? Where did the review agent match or exceed human review, and could the repo grant it more autonomy for that class of change? Use the `autonomy-readiness` skill for structured analysis.
```

- [ ] **Step 2: Run scaffold tests to confirm the agent definition still loads**

Run: `go test ./internal/scaffold/ -run TestHarnessesLoadAndValidate -v`
Expected: PASS — the harness validation confirms the agent file path is valid and the file is readable.

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/agents/retro.md
git commit -S -s -m "feat(retro): add autonomy readiness as optimization goal

Directs the retro agent to analyze the delta between agent review and
human review, using the autonomy-readiness skill for structured analysis.
Proposals from this lens compete for the standard 3-proposal budget.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

### Task 4: Update the retro agent skills list in frontmatter

**Files:**
- Modify: `internal/scaffold/fullsend-repo/agents/retro.md:1-15` (frontmatter)

- [ ] **Step 1: Add autonomy-readiness to the agent frontmatter skills list**

The agent frontmatter currently lists:

```yaml
skills:
  - retro-analysis
  - finding-agent-runs
```

Change it to:

```yaml
skills:
  - retro-analysis
  - finding-agent-runs
  - autonomy-readiness
```

Note: The agent frontmatter uses short names (without the `skills/` prefix), while the harness uses full paths. Follow the existing pattern.

- [ ] **Step 2: Run scaffold tests**

Run: `go test ./internal/scaffold/ -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/scaffold/fullsend-repo/agents/retro.md
git commit -S -s -m "feat(retro): list autonomy-readiness in agent frontmatter

Adds the skill to the agent's declared skill list so it appears in
the agent's system prompt alongside retro-analysis and finding-agent-runs.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```

### Task 5: Add the skill to the scaffold file existence test

**Files:**
- Modify: `internal/scaffold/scaffold_test.go` (TestFullsendRepoFilesExist)

- [ ] **Step 1: Read the current test to find the skills entries**

Read `internal/scaffold/scaffold_test.go` and find the `TestFullsendRepoFilesExist` function. Locate the list of expected files that includes other skill paths like `skills/code-implementation/SKILL.md` and `skills/issue-labels/SKILL.md`.

- [ ] **Step 2: Add the new skill to the expected files list**

Add `"skills/autonomy-readiness/SKILL.md"` to the expected files list, following the alphabetical or existing ordering convention used by other skill entries.

- [ ] **Step 3: Run the test**

Run: `go test ./internal/scaffold/ -run TestFullsendRepoFilesExist -v`
Expected: PASS

- [ ] **Step 4: Run the full scaffold test suite**

Run: `go test ./internal/scaffold/ -v`
Expected: PASS — all tests pass, including the new file existence check.

- [ ] **Step 5: Commit**

```bash
git add internal/scaffold/scaffold_test.go
git commit -S -s -m "test: add autonomy-readiness skill to scaffold file checks

Ensures the skill directory and SKILL.md are included in the scaffold
file existence test alongside other skills.

Assisted-by: Claude claude-opus-4-6 <noreply@anthropic.com>"
```
