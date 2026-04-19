# Enrollment Reconciliation Design

**Date:** 2026-04-18
**Status:** Approved

## Problem

The enrollment system only enrolls repos with `enabled: true`. Repos set to `enabled: false` in `config.yaml` keep their shim workflows forever. There is no way to disconnect a repo from the fullsend pipeline without manually deleting the shim.

## Decision

Extend the enrollment script to reconcile both directions: enroll enabled repos, unenroll disabled repos.

## Design

### Script: rename `enroll-repos.sh` to `reconcile-repos.sh`

The script validates repo names from config.yaml against `^[a-zA-Z0-9._-]+$` before using them in API calls, preventing slash injection. This validation applies to both phases.

The script runs in two phases.

**Phase 1 — Enroll** (existing logic, plus cross-direction cleanup): Loop over repos with `enabled: true`. For each repo:

1. Close any open removal PR on `fullsend/offboard` (a previous disable cycle may have left one). Delete the `fullsend/offboard` branch if it exists.
2. Continue with existing enrollment logic: check whether the shim exists on the default branch (skip if so), check for an existing enrollment PR (update shim if so), or create a new enrollment branch and PR.

**Phase 2 — Unenroll** (new): Extract disabled repos with `yq '.repos | to_entries[] | select(.value.enabled == false) | .key'`. For each disabled repo:

1. Close any open enrollment PR on `fullsend/onboard` with a comment that includes the triggering commit SHA (`$GITHUB_SHA`) for audit trail. Delete the `fullsend/onboard` branch.
2. Check for an existing open removal PR on `fullsend/offboard`. If one exists, skip (unenrollment is already pending).
3. Check whether the shim exists on the default branch. If not, skip (already unenrolled).
4. Create a removal branch `fullsend/offboard` from the default branch tip (create or force-update, same pattern as enrollment).
5. Fetch the file SHA from the removal branch: `gh api repos/$ORG/$REPO/contents/$SHIM_PATH?ref=fullsend/offboard --jq .sha`. If the file is not found (404), skip — the shim was already removed.
6. Delete `.github/workflows/fullsend.yaml` on the removal branch via the Contents API DELETE endpoint, passing the SHA.
7. Open a removal PR titled "Disconnect from fullsend agent pipeline" with a body explaining the repo was disabled in config.yaml.

The summary adds an `Unenrolled` counter alongside `Enrolled`, `Skipped`, and `Failed`.

No new `forge.Client` interface methods are required. All unenrollment operations are performed by the script via `gh api`/`gh pr` commands.

### Workflow: `repo-maintenance.yml`

Update the step to call `reconcile-repos.sh` instead of `enroll-repos.sh`.

### Normative spec updates

The following files reference `enroll-repos.sh` and must be updated to `reconcile-repos.sh`:

- `docs/normative/admin-install/v1/adr-0012-fullsend-repo-files/SPEC.md`
- `docs/normative/admin-install/v1/adr-0013-enrollment/SPEC.md`
- `internal/layers/enrollment.go` (comment referencing script name)

### Go: `config.DisabledRepos()`

Add a `DisabledRepos()` method to `OrgConfig` that returns repos where `enabled: false`, sorted. Mirrors the existing `EnabledRepos()`.

### Go: `EnrollmentLayer`

- `NewEnrollmentLayer` gains a `disabledRepos []string` parameter. All call sites passing `nil` for enabled repos should also pass `nil` for disabled repos (the parameter is additive and nil-safe for uninstall/verify paths).
- `Install` — The early return changes from `len(enabledRepos) == 0` to `len(enabledRepos) == 0 && len(disabledRepos) == 0`, so the workflow is dispatched when there are disabled repos to reconcile even if no repos are enabled. `reportReconciliationPRs` (renamed from `reportEnrollmentPRs`) checks both enabled and disabled repos for enrollment and removal PRs respectively.
- `Analyze` — Checks disabled repos too. A disabled repo with a shim is `StatusDegraded` with `WouldFix: "create removal PR for <repo>"`. A disabled repo without a shim is healthy.
- `Uninstall` — Stays a no-op.

Call sites requiring the new parameter:

| Location | File | Change |
|---|---|---|
| `buildLayerStack` | `internal/cli/admin.go` | Pass `disabledRepos` |
| `runUninstall` | `internal/cli/admin.go` | Pass `nil` |
| `buildTestLayerStack` | `e2e/admin/admin_test.go` | Pass `disabledRepos` |
| `runUninstall` | `e2e/admin/admin_test.go` | Pass `nil` |
| `runUninstallAllowNotFound` | `e2e/admin/admin_test.go` | Pass `nil` |
| `verifyNotInstalled` | `e2e/admin/admin_test.go` | Pass `nil` |

### Go: `admin.go`

Pass both `enabledRepos` and `disabledRepos` when constructing `EnrollmentLayer`.

### E2E test

Extend the existing test with a new phase:

- **Phase 2.5** (unchanged): Merge enrollment PR, create test issue, verify triage dispatch.
- **Phase 2.75** (new): Set `test-repo` to `enabled: false` in config.yaml, run install, assert a removal PR exists on `test-repo` with title "Disconnect from fullsend agent pipeline", merge it, verify the shim no longer exists on the default branch.
- **Phase 3+** (unchanged): Uninstall, verify idempotency.

**cleanup.go**: Must also delete the `fullsend/offboard` branch and close stale removal PRs titled "Disconnect from fullsend agent pipeline", mirroring the existing cleanup for `fullsend/onboard`.

### Unit tests

- Existing enrollment tests updated for the new constructor signature.
- New Analyze tests: disabled repo with shim returns `StatusDegraded` + `WouldFix`; disabled repo without shim is healthy.
- Test that `reportReconciliationPRs` picks up both enrollment and removal PR titles.
- Test `config.DisabledRepos()`.

## Alternatives considered

**Separate `unenroll-repos.sh` script.** Rejected — duplicates boilerplate (config parsing, org detection, summary reporting) and adds a second script to maintain.

**Go-side unenrollment in EnrollmentLayer.** Rejected — breaks the architecture where the CLI dispatches and the workflow does the work. The CLI does not have the fullsend app token; only the workflow generates one via `actions/create-github-app-token`.

**Discovery-based reconciliation** (scan all org repos for stale shims). Rejected — expensive org-wide scan, unpredictable. If someone removes a repo from config, they can add it back as `enabled: false` to trigger cleanup.
