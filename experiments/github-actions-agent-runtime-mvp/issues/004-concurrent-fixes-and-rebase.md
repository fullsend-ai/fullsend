# 004: Concurrent Bot + Human Fixes Conflict

## Problem

Human-triggered (`/fix-agent` or `workflow_dispatch`) and bot-triggered (`pull_request_review`) fix agents share the same concurrency group (`fullsend-fix-pr-N`). The newer run cancels the older one, even when they address independent issues.

## Observed ([PR #7](https://github.com/nonflux/integration-service/pull/7))

Human triggered fix for linter issues. 4 minutes later, review agent submitted `CHANGES_REQUESTED`, triggering a bot fix that cancelled the human fix. The two fixes were independent.

## Proposed Fix

1. **Separate concurrency groups by trigger type** — `fix-bot-pr-N` vs `fix-manual-pr-N`
2. **Rebase on push conflict** — if `git push` fails because the other agent pushed first, `git pull --rebase` and retry
3. **Conflict reporting** — if rebase fails, post conflicting files and escalate to human
