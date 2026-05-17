# Bugfix workflow

How fullsend handles a bug report from issue creation to merged fix, end to end. This guide is for developers working in a repo where fullsend is [installed and enrolled](../admin/installation.md).

## Overview

When someone files a bug, fullsend's agent pipeline processes it through three stages:

1. **Triage** — validates the issue, checks for duplicates, attempts reproduction
2. **Code** — implements a fix, writes tests, opens a PR, passes CI
3. **Review** — multiple review agents evaluate the PR independently, a coordinator decides the outcome

Each stage is triggered by labels and can be restarted with slash commands. The pipeline uses GitHub's native primitives (issues, PRs, labels, branch protection) as its coordination layer — there is no central orchestrator. See [ADR 0002](../../ADRs/0002-initial-fullsend-design.md) for the full design.

```
Issue filed → Triage → ready-to-code → Code Agent → PR opened → Review → ready-for-merge → Merge
                │                          ↑                                │
                │                          └── changes requested (planned) ─┘
                ├── blocked → waiting for dependency
                ├── duplicate → closed
                ├── not-ready → waiting for info
                └── not-reproducible → human intervention
```

> **Note:** The automated rework loop (Review → Code Agent on "changes requested") is not yet implemented. Today, a "changes requested" outcome requires human intervention. The planned [fix agent (#197)](https://github.com/fullsend-ai/fullsend/issues/197) will automate this loop.

## What you need to know as a developer

### Writing good bug reports

The triage agent reads the issue title, body, comments, and GitHub-native attachments. This means:

- Put key information in the issue body — expected behavior, actual behavior, steps to reproduce, version/environment.
- Use GitHub's native file attachments for logs, screenshots, or reproduction scripts.
- You can add details via comments — the triage agent reads those too. Other users can also comment with additional context (e.g., confirming the bug on a different platform).
- Editing the issue title or body triggers triage automatically. You can also use `/fs-triage` to force a fresh run.

### Labels are the state machine

These labels track where an issue is in the pipeline:

| Label | Meaning | What happens next |
|-------|---------|-------------------|
| `blocked` | Progress depends on another issue or PR | Triage comment links to the blocker; re-triage on edit checks if blocker is resolved |
| `duplicate` | Same issue already tracked elsewhere | Issue closed, link to canonical issue |
| `not-ready` | Missing information | Triage comment explains what's needed; add a comment or edit the issue body to fix |
| `not-reproducible` | Bug couldn't be reproduced in the sandbox | Human intervention required; triage comment documents what was tried |
| `ready-to-code` | Triage passed | Code agent picks it up |
| `ready-for-review` | PR ready for review (manual trigger) | Review agents evaluate the PR |
| `ready-for-merge` | All reviewers unanimously approved | PR can be merged per governance policy |
| `requires-manual-review` | Reviewers disagreed or flagged security concerns | Human must decide |

Labels are mutually exclusive where it matters — the pipeline enforces this. You generally don't need to manage labels manually.

### Slash commands

You can control the pipeline from issue or PR comments:

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-triage` | Issue comment | Re-runs triage from scratch (clears all labels, reopens if closed) |
| `/fs-code` | Issue comment | Hands off to the code agent (expects `ready-to-code` or forces with human ack) |
| `/fs-review` | PR comment | Enqueues a new review round for the current PR head |
| `/fs-retro` | Issue or PR comment | Triggers a retrospective analysis of the workflow |

### What to expect from agent PRs

When the code agent opens a PR:

- The PR links back to the originating issue.
- The PR description summarizes what was changed and why.
- The code agent has already run the test suite in its sandbox and iterated until tests pass.
- After pushing, GitHub's required checks run. If checks fail, the code agent fetches logs, fixes the issue, and pushes again (up to a configurable retry cap).
- Once checks are green, the review agents take over automatically (triggered by the PR creation or push event).

### Reviewing agent output

Agent PRs go through the same review process as human PRs:

- **CODEOWNERS still applies.** If your repo has CODEOWNERS rules, the required human reviewers must still approve — agents cannot bypass this.
- **Branch protection still applies.** Required checks, review counts, and merge restrictions are unchanged.
- **Read the diff.** Agent code is functional but may not match your team's style preferences. Treat it like any other PR.

### Review outcomes

The review stage runs N independent review agents in parallel. One is randomly selected as coordinator. The coordinator collects verdicts and applies one of three outcomes:

- **Unanimous approve:** All reviewers agree the PR is good. Label `ready-for-merge` is applied. The PR can be merged per your org's governance policy.
- **Unanimous rework:** All reviewers agree changes are needed. Label `ready-to-code` is re-applied. Today, a human must address the review feedback manually. When the [fix agent (#197)](https://github.com/fullsend-ai/fullsend/issues/197) is implemented, this rework loop will be automated.
- **Split or conflicting:** Reviewers disagree, or there are conflicting security assessments. Label `requires-manual-review` is applied. A human must decide.

Every push to a PR in the review stage triggers a new review round. This means `ready-for-merge` is never stale — it always reflects the current PR head.

> **Planned:** The **fix agent** ([#197](https://github.com/fullsend-ai/fullsend/issues/197)) will handle the rework loop automatically. When a review agent requests changes or a human posts `/fs-fix [instruction]`, the fix agent reads the review feedback and pushes fixes to the existing PR — no manual coding required. The fix agent is a separate workflow from the code agent, with its own prompt scoped to "read review feedback, fix existing PR."

## The stages in detail

### Stage 1: Triage

**Triggered by:** issue creation, issue title/body edit, or `/fs-triage` command.

The triage agent:

1. **Checks for duplicates.** Searches existing issues by title, body, and metadata. If it finds a match with high confidence, it labels `duplicate`, posts a comment linking the canonical issue, and closes this one.
2. **Checks for blocking dependencies.** Searches for open issues or PRs (in this repo or upstream) that must be resolved before work can start. If a blocker is found, it labels `blocked` and posts a comment linking to the blocking issue or PR. On re-triage, it checks whether existing blockers have been resolved.
3. **Checks information sufficiency.** If the issue body is missing steps to reproduce, expected behavior, or other critical details, it labels `not-ready` and posts a comment explaining what's missing.
4. **Attempts reproduction.** Runs the reported steps in an isolated sandbox. If the bug cannot be reproduced, it labels `not-reproducible` and posts a detailed comment documenting what was tried.
5. **Produces a test artifact.** When possible, writes a failing test case aligned with the repo's test framework.
6. **Hands off.** Labels `ready-to-code` with a summary comment.

**If triage gets it wrong:** Add a comment with the missing information, or edit the issue body. Edits to the title or body trigger triage automatically. You can also use `/fs-triage` to force a fresh run — this clears all previous labels and starts from scratch.

### Stage 2: Code

**Triggered by:** `ready-to-code` label or `/fs-code` command.

The code agent:

1. **Reads the handoff.** Issue title, body, attachments, and triage output comments.
2. **Branches and implements.** Creates a branch, writes the fix following repo conventions.
3. **Tests iteratively.** Runs the test suite, incorporates triage-provided tests if present, writes new tests if needed. Iterates until tests pass.
4. **Opens a PR.** Links the issue, describes the changes.
5. **Handles CI failures.** Fetches failing check logs, fixes issues, pushes again. Repeats until all required checks pass (up to a configurable cap, default defined in `config.yaml` as `defaults.max_implementation_retries`).
6. **Hands off to review.** The PR creation or push triggers review dispatch automatically via `pull_request_target`.

### Stage 3: Review

**Triggered by:** `pull_request_target` events (PR opened, push to PR branch, or marked ready for review), `/fs-review` command, or `ready-for-review` label.

The review swarm:

1. **N independent reviewers** evaluate the PR in parallel (configurable count).
2. **One coordinator** (randomly selected) collects verdicts and posts a consolidated comment.
3. **Outcome** is applied as a label: `ready-for-merge`, `ready-to-code` (rework), or `requires-manual-review`.

Re-review happens automatically on every push to the PR. The `ready-for-merge` label is scoped to the PR head SHA at the time of review — it is cleared and re-evaluated on each new round.

### After merge

Once the PR is merged (by human, merge queue, or automation per org governance), the automated pipeline for this issue is complete.

The **retro agent** ([#131](https://github.com/fullsend-ai/fullsend/issues/131)) runs automatically when a PR is closed (merged or rejected) and can also be triggered on-demand via `/fs-retro` on any issue or PR comment. It analyzes the full workflow graph — triage, code, review, and fix agent interactions plus any human interventions — to identify improvement opportunities. Proposals are filed as GitHub issues in the appropriate repo, and a summary comment is posted on the originating PR/issue linking to all proposals.

## Intervening in the pipeline

### Stopping automation

- Remove the triggering label (`ready-to-code`) to prevent the next stage from starting. Note: review is triggered automatically by PR events (`pull_request_target`), so closing the PR is the way to stop review dispatch.
- Close the issue. Agents don't act on closed issues (except `/fs-triage` which explicitly reopens).

### Restarting a stage

- `/fs-triage` — wipes all labels, reopens the issue, runs triage fresh.
- `/fs-code` — restarts the code agent from the current issue state.
- `/fs-review` — enqueues a new review round.

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
