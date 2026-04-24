# Bugfix workflow

How fullsend handles a bug report from issue creation to merged fix, end to end. This guide is for developers working in a repo where fullsend is [installed and enrolled](../admin/installation.md).

## Overview

When someone files a bug, fullsend's agent pipeline processes it through three stages:

1. **Triage** — validates the issue, checks for duplicates, attempts reproduction
2. **Code** — implements a fix, writes tests, opens a PR, passes CI
3. **Review** — multiple review agents evaluate the PR independently, a coordinator decides the outcome

Each stage is triggered by labels and can be restarted with slash commands. The pipeline uses GitHub's native primitives (issues, PRs, labels, branch protection) as its coordination layer — there is no central orchestrator. See [ADR 0002](../../ADRs/0002-initial-fullsend-design.md) for the full design.

```
Issue filed → Triage → ready-to-code → Code Agent → ready-for-review → Review → ready-for-merge → Merge
                │                          ↑                              │
                │                          └──────── changes requested ───┘
                ├── blocked → waiting for dependency
                ├── duplicate → closed
                ├── not-ready → waiting for info
                └── not-reproducible → human intervention
```

## What you need to know as a developer

### Writing good bug reports

The triage agent reads **only** the issue title, body, and GitHub-native attachments. It does not read comments. This means:

- Put all relevant information in the issue body — expected behavior, actual behavior, steps to reproduce, version/environment.
- Use GitHub's native file attachments for logs, screenshots, or reproduction scripts.
- If you need to update the report, **edit the issue body**, don't add a comment. Edits to the title or body trigger triage automatically.

### Labels are the state machine

These labels track where an issue is in the pipeline:

| Label | Meaning | What happens next |
|-------|---------|-------------------|
| `blocked` | Progress depends on another issue or PR | Triage comment links to the blocker; re-triage on edit checks if blocker is resolved |
| `duplicate` | Same issue already tracked elsewhere | Issue closed, link to canonical issue |
| `not-ready` | Missing information | Triage comment explains what's needed; edit the issue body to fix |
| `not-reproducible` | Bug couldn't be reproduced in the sandbox | Human intervention required; triage comment documents what was tried |
| `ready-to-code` | Triage passed | Code agent picks it up |
| `ready-for-review` | PR with passing CI ready for review | Review agents evaluate the PR |
| `ready-for-merge` | All reviewers unanimously approved | PR can be merged per governance policy |
| `requires-manual-review` | Reviewers disagreed or flagged security concerns | Human must decide |

Labels are mutually exclusive where it matters — the pipeline enforces this. You generally don't need to manage labels manually.

### Slash commands

You can control the pipeline from issue or PR comments:

| Command | Where | Effect |
|---------|-------|--------|
| `/triage` | Issue comment | Re-runs triage from scratch (clears all labels, reopens if closed) |
| `/implement` | Issue comment | Hands off to the code agent (expects `ready-to-code` or forces with human ack) |
| `/review` | PR comment | Enqueues a new review round for the current PR head |

### What to expect from agent PRs

When the code agent opens a PR:

- The PR links back to the originating issue.
- The PR description summarizes what was changed and why.
- The code agent has already run the test suite in its sandbox and iterated until tests pass.
- After pushing, GitHub's required checks run. If checks fail, the code agent fetches logs, fixes the issue, and pushes again (up to a configurable retry cap).
- Once checks are green, the PR is labeled `ready-for-review` and the review agents take over.

### Reviewing agent output

Agent PRs go through the same review process as human PRs:

- **CODEOWNERS still applies.** If your repo has CODEOWNERS rules, the required human reviewers must still approve — agents cannot bypass this.
- **Branch protection still applies.** Required checks, review counts, and merge restrictions are unchanged.
- **Read the diff.** Agent code is functional but may not match your team's style preferences. Treat it like any other PR.

### Review outcomes

The review stage runs N independent review agents in parallel. One is randomly selected as coordinator. The coordinator collects verdicts and applies one of three outcomes:

- **Unanimous approve:** All reviewers agree the PR is good. Label `ready-for-merge` is applied. The PR can be merged per your org's governance policy.
- **Unanimous rework:** All reviewers agree changes are needed. Label `ready-to-code` is re-applied and the code agent resumes work.
- **Split or conflicting:** Reviewers disagree, or there are conflicting security assessments. Label `requires-manual-review` is applied. A human must decide.

Every push to a PR in the review stage triggers a new review round. This means `ready-for-merge` is never stale — it always reflects the current PR head.

> **Planned:** The **fix agent** ([#197](https://github.com/fullsend-ai/fullsend/issues/197)) will handle the rework loop automatically. When a review agent requests changes or a human posts `/fix-agent [instruction]`, the fix agent reads the review feedback and pushes fixes to the existing PR — no manual coding required. The fix agent is a separate workflow from the code agent, with its own prompt scoped to "read review feedback, fix existing PR."

## The stages in detail

### Stage 1: Triage

**Triggered by:** issue creation, issue title/body edit, or `/triage` command.

The triage agent:

1. **Checks for duplicates.** Searches existing issues by title, body, and metadata. If it finds a match with high confidence, it labels `duplicate`, posts a comment linking the canonical issue, and closes this one.
2. **Checks for blocking dependencies.** Searches for open issues or PRs (in this repo or upstream) that must be resolved before work can start. If a blocker is found, it labels `blocked` and posts a comment linking to the blocking issue or PR. On re-triage, it checks whether existing blockers have been resolved.
3. **Checks information sufficiency.** If the issue body is missing steps to reproduce, expected behavior, or other critical details, it labels `not-ready` and posts a comment explaining what's missing.
4. **Attempts reproduction.** Runs the reported steps in an isolated sandbox. If the bug cannot be reproduced, it labels `not-reproducible` and posts a detailed comment documenting what was tried.
5. **Produces a test artifact.** When possible, writes a failing test case aligned with the repo's test framework.
6. **Hands off.** Labels `ready-to-code` with a summary comment.

**If triage gets it wrong:** Edit the issue body with better information and triage re-runs automatically. Or use `/triage` to force a fresh run — this clears all previous labels and starts from scratch.

### Stage 2: Code

**Triggered by:** `ready-to-code` label or `/implement` command.

The code agent:

1. **Reads the handoff.** Issue title, body, attachments, and triage output comments.
2. **Branches and implements.** Creates a branch, writes the fix following repo conventions.
3. **Tests iteratively.** Runs the test suite, incorporates triage-provided tests if present, writes new tests if needed. Iterates until tests pass.
4. **Opens a PR.** Links the issue, describes the changes.
5. **Handles CI failures.** Fetches failing check logs, fixes issues, pushes again. Repeats until all required checks pass (up to a configurable cap, default defined in `config.yaml` as `defaults.max_implementation_retries`).
6. **Hands off to review.** Labels `ready-for-review`.

### Stage 3: Review

**Triggered by:** `ready-for-review` label, `/review` command, or push to the PR branch.

The review swarm:

1. **N independent reviewers** evaluate the PR in parallel (configurable count).
2. **One coordinator** (randomly selected) collects verdicts and posts a consolidated comment.
3. **Outcome** is applied as a label: `ready-for-merge`, `ready-to-code` (rework), or `requires-manual-review`.

Re-review happens automatically on every push to the PR. The `ready-for-merge` label is scoped to the PR head SHA at the time of review — it is cleared and re-evaluated on each new round.

### After merge

Once the PR is merged (by human, merge queue, or automation per org governance), the automated pipeline for this issue is complete.

> **Planned:** The **retro agent** ([#131](https://github.com/fullsend-ai/fullsend/issues/131)) will capture lessons learned from the pipeline run — review rejections, CI failures, manual interventions — and feed them back into the agent harness configuration. This lets the system improve over time without manual prompt tuning. Feedback is scoped per-repo, with optional org-wide promotion, and maintainers can review corrections before they take effect.

## Intervening in the pipeline

### Stopping automation

- Remove the triggering label. Without `ready-to-code` or `ready-for-review`, the next stage won't fire.
- Close the issue. Agents don't act on closed issues (except `/triage` which explicitly reopens).

### Restarting a stage

- `/triage` — wipes all labels, reopens the issue, runs triage fresh.
- `/implement` — restarts the code agent from the current issue state.
- `/review` — enqueues a new review round.

### Taking over manually

At any point you can:

1. Push commits to the agent's PR branch — the review agents will re-review.
2. Close the agent's PR and open your own — the issue labels are your entry point.
3. Remove the `ready-to-code` label to prevent the code agent from starting, then implement the fix yourself.

Fullsend does not lock you out. The labels are the state machine, and you have full control over them.

## Reference

- [ADR 0002](../../ADRs/0002-initial-fullsend-design.md) — initial fullsend design (full workflow specification)
- [Architecture overview](../../architecture.md) — component vocabulary and execution stack
- [Installing fullsend](../admin/installation.md) — prerequisite: admin setup guide
- [Security threat model](../../problems/security-threat-model.md) — how fullsend thinks about security
