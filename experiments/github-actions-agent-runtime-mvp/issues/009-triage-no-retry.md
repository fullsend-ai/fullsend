# 009: Triage Agent Only Triggers on `issues.opened`

## Problem

The triage agent workflow only triggers on `issues: types: [opened]`. If triage fails (workflow bug, transient error, API timeout), there is no way to re-trigger it on the same issue without creating a duplicate.

## Observed

Triage agent failed on [Issue #8](https://github.com/nonflux/integration-service/issues/8) due to invalid `github_issue_number` input. After the workflow fix was merged, the issue could not be re-triaged.

## Proposed Fix

Add `reopened` to the trigger:

```yaml
on:
  issues:
    types: [opened, reopened]
```

Maintainers can close and reopen an issue to re-trigger triage. The triage logic is idempotent (uses in-place comment updates), so re-running is safe.
