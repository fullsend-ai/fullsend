# Silent skip for unconfigured roles in dispatch workflows

**Issue:** [#973](https://github.com/fullsend-ai/fullsend/issues/973)
**Date:** 2026-05-15

## Problem

The shim workflow dispatches `stage=retro` on every PR close event. On orgs that have not configured the retro agent (no `fullsend` role in `defaults.roles`), the `defaults.roles` gate in `dispatch.yml` rejects the dispatch with `exit 1` and an `::error::` annotation. This produces a failed workflow run on every merged PR, creating noise in the Actions tab.

## Design

Change the role-not-configured check in both dispatch workflows from a hard failure to a silent skip.

### What changes

In the "Check role is enabled" step of both files:

- **Before:** `::error::` annotation + `exit 1`
- **After:** `::notice::` annotation + clear the `stage` output + `exit 0`

Clearing the `stage` output (`echo "stage=" >> "${GITHUB_OUTPUT}"`) ensures downstream jobs gated on `needs.route.outputs.stage == '<stage>'` are skipped cleanly.

### Files to modify

1. `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml` (per-org mode) — lines 265-266
2. `.github/workflows/reusable-dispatch.yml` (per-repo mode) — lines 250-251

### What stays the same

- The kill switch remains `exit 1`. It is an intentional "stop everything" signal.
- The role gate still blocks dispatch for unconfigured roles. The behavior is identical — only the exit code and annotation level change.

## Test plan

- Update `internal/scaffold/scaffold_test.go` if it asserts on the error message text.
- Verify the new notice message text appears in the workflow output.
- Scenarios:
  1. Stage dispatched when its role is NOT in `defaults.roles` — workflow exits 0, notice annotation logged, no error.
  2. Stage dispatched when its role IS in `defaults.roles` — workflow proceeds normally.
  3. Empty `defaults.roles` — all stages skip silently.
