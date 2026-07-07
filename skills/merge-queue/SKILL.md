---
name: merge-queue
description: >-
  Use when you need to add a PR to a GitHub merge queue, check what's currently
  queued, or find out why a PR was removed from the queue. The gh CLI has no
  built-in merge-queue commands, so this skill provides scripts that use the
  GraphQL API.
allowed-tools: Bash(bash skills/merge-queue/scripts/*:*)
---

# Merge Queue

## Enqueue a PR

Run `bash skills/merge-queue/scripts/enqueue-pr.sh [PR_NUMBER_OR_URL]` to enqueue a PR.
Omit the argument to enqueue the current branch's PR.

If the PR is not yet eligible (checks pending, missing approvals), use
`await-and-enqueue.sh` instead — see below.

### Accepted input formats

- **PR number:** `652` (uses the current repo context from `gh`)
- **PR URL:** `https://github.com/owner/repo/pull/652`
- **Omitted:** uses the current branch's PR

The `owner/repo#number` format is **not supported** — use a URL or number instead.

## Check queue status

Run `bash skills/merge-queue/scripts/queue-status.sh [OWNER/REPO] [BRANCH]` to list PRs currently in the merge queue.

Both arguments are optional — defaults to the current repo and `main` branch.

Shows each entry's position, state, PR title/URL, author, enqueuer, and estimated time to merge.

## Investigate dequeue reasons

Run `bash skills/merge-queue/scripts/dequeue-reason.sh <PR_NUMBER_OR_URL>` to find out why a PR was removed from the merge queue.

Shows each removal event's timestamp, reason (e.g. `failed_checks`, `merge_conflict`), and the commit SHA at the time of removal.

## Await and enqueue

Run `bash skills/merge-queue/scripts/await-and-enqueue.sh [PR_NUMBER_OR_URL]` to
poll a PR until all required checks pass and the PR is approved, then
automatically enqueue it. Exits early if any check fails.

Use this when `enqueue-pr.sh` rejects a PR because checks are still pending.
GitHub's `auto-merge` API (`gh pr merge --auto`) does not work with merge
queues, so this script fills that gap.

Set `POLL_INTERVAL` (default: 30 seconds) to control how often it checks.

## Prerequisites

- `gh` CLI authenticated with write access to the target repository
- `jq` installed
- The target repository must have merge queues enabled in its branch protection rules

## Common errors

- **"Pull request is already in the merge queue"** — the PR was previously enqueued; no action needed.
- **"Pull request is not mergeable"** — the PR may need approvals, passing checks, or conflict resolution before it can be enqueued.
- **"Resource not accessible by integration"** — the `gh` token lacks sufficient permissions.
- **"status checks are expected"** — required checks haven't finished yet. Use `await-and-enqueue.sh` to poll and enqueue once they pass.
- **`gh pr merge --auto` fails with merge queues** — GitHub's auto-merge API does not support merge queues. Use `await-and-enqueue.sh` instead.
