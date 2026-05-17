---
name: merge-queue
description: >-
  Use when you need to add a PR to a GitHub merge queue. The gh CLI has no
  built-in merge-queue command, so this skill provides a script that uses the
  GraphQL API.
allowed-tools: Bash(bash skills/merge-queue/scripts/enqueue-pr.sh:*)
---

# Merge Queue

Run `bash skills/merge-queue/scripts/enqueue-pr.sh [PR_NUMBER_OR_URL]` to enqueue a PR.
Omit the argument to enqueue the current branch's PR.

## Accepted input formats

- **PR number:** `652` (uses the current repo context from `gh`)
- **PR URL:** `https://github.com/owner/repo/pull/652`
- **Omitted:** uses the current branch's PR

The `owner/repo#number` format is **not supported** — use a URL or number instead.

## Prerequisites

- `gh` CLI authenticated with write access to the target repository
- `jq` installed
- The target repository must have merge queues enabled in its branch protection rules

## Common errors

- **"Pull request is already in the merge queue"** — the PR was previously enqueued; no action needed.
- **"Pull request is not mergeable"** — the PR may need approvals, passing checks, or conflict resolution before it can be enqueued.
- **"Resource not accessible by integration"** — the `gh` token lacks sufficient permissions.
