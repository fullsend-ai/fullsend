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

Run `bash skills/merge-queue/scripts/enqueue-pr.sh [PR_NUMBER] [-R owner/repo]` to
enqueue a PR. Omit the argument to enqueue the current branch's PR.

If the PR is not yet eligible (checks pending, missing approvals), use
`await-and-enqueue.sh` instead — see below.

### Accepted input formats

- **PR number:** `652` (uses the current repo context from `gh`)
- **PR number + repo:** `652 -R owner/repo` (a PR in a different repo)
- **Omitted:** uses the current branch's PR

The `owner/repo#number` format is **not supported** — use a number (with
`-R` for another repo) instead.

**Do not pass a raw PR URL** (`https://github.com/owner/repo/pull/652`) as
the argument to this script's Bash tool invocation. The sandbox's SSRF
PreToolUse hook inspects the *outer* `bash skills/merge-queue/scripts/...`
command before the script runs; `bash` is not on the hook's inert-command
list, so a `github.com` URL literal anywhere on that command line is
DNS-resolved and fail-closed (`github.com` is not egress-allowlisted) —
regardless of how the script itself later parses its arguments. Always use
the PR number (+ `-R owner/repo` for a PR outside the current repo). If all
you have is a URL, extract the number and `owner/repo` first with an
inert-only Bash tool call (`echo`, `grep`, and `cut` only — no `bash` or
`gh`):

```
match="$(echo "$URL" | grep -oE '[^/]+/[^/]+/pull/[0-9]+')"
nwo="$(echo "$match" | cut -d/ -f1-2)"
num="$(echo "$match" | cut -d/ -f4)"
```

The pattern is intentionally unanchored so it still matches URLs with a
trailing path segment (e.g. `.../pull/652/files`, `/commits`, `/checks`).
Then, as a **separate** Bash tool call containing no URL literal, invoke
the script with the extracted values:

```
bash skills/merge-queue/scripts/enqueue-pr.sh "$num" -R "$nwo"
```

## Check queue status

Run `bash skills/merge-queue/scripts/queue-status.sh [OWNER/REPO] [BRANCH]` to list PRs currently in the merge queue.

Both arguments are optional — defaults to the current repo and `main` branch.

Shows each entry's position, state, PR title/URL, author, enqueuer, and estimated time to merge.

## Investigate dequeue reasons

Run `bash skills/merge-queue/scripts/dequeue-reason.sh <PR_NUMBER> [-R owner/repo]`
to find out why a PR was removed from the merge queue. Use `-R owner/repo`
for a PR in a different repo — see the warning above (under "Enqueue a PR")
about never passing a raw PR URL to the script's Bash tool invocation.

Shows each removal event's timestamp, reason (e.g. `failed_checks`, `merge_conflict`), and the commit SHA at the time of removal.

## Await and enqueue

Run `bash skills/merge-queue/scripts/await-and-enqueue.sh [PR_NUMBER] [-R owner/repo]`
to poll a PR until all required checks pass and the PR is approved, then
automatically enqueue it. Exits early if any check fails. Accepts the same
input formats as `enqueue-pr.sh` above — see the warning there about never
passing a raw PR URL to the script's Bash tool invocation.

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
