# 014: Triage Agent Multi-Label Race Condition

## Problem

The triage agent adds multiple labels simultaneously (e.g., `bug`, `kind/bug`, `area/controller`, `priority/high`, `ready-for-implementation`). Each label addition fires a separate `issues.labeled` event. Since the implementation agent triggers on `issues.labeled` with the `ready-for-implementation` label, multiple concurrent runs are created — and the concurrency group (`fullsend-implement-issue-N`, `cancel-in-progress: true`) cancels all but the last one.

## Observed ([Issue #19](https://github.com/nonflux/integration-service/issues/19))

Triage agent added 6 labels. 5 implementation agent runs were created — all cancelled/skipped by the concurrency group. The pipeline only progressed after manually removing and re-adding the `ready-for-implementation` label.

## Proposed Fixes

1. **Add `ready-for-implementation` last** — update triage agent prompt to add this label as a separate step after all other labels
2. **Delay in workflow** — add a short delay (`sleep 10`) at the start of the implementation agent to let the concurrency group settle
3. **Use `workflow_dispatch`** — have the triage agent explicitly trigger the implementation agent instead of relying on label events
