# Reconcile Org Secret Access

**Date:** 2026-04-23
**Status:** Approved
**Relates to:** https://github.com/konflux-ci/caching/actions/runs/24844542404/job/72727757382?pr=781

## Problem

The `FULLSEND_DISPATCH_TOKEN` org secret has visibility "selected", scoped to specific repos. The installer sets the repo access list at install time, but when `reconcile-repos.sh` later enrolls new repos, it never updates the access list. Newly enrolled repos cannot see the secret, so their shim workflows fail with `GH_TOKEN` not set.

## Design

Move org secret repo access management from the installer into the reconcile loop, so access stays in sync with enrollment state.

### 1. Add `organization_secrets` permission to the fullsend app

Add `OrganizationSecrets: "write"` to the `AppPermissions` struct and to the `fullsend` role in `AgentAppConfig`. This lets the app token generated in `repo-maintenance.yml` manage org secret visibility.

**Files:** `internal/forge/github/types.go`

### 2. Manage secret access in `reconcile-repos.sh`

After enrolling a repo, grant it access to the dispatch token:

```
gh api "orgs/$ORG/actions/secrets/FULLSEND_DISPATCH_TOKEN/repositories/$REPO_ID" --method PUT --silent
```

After unenrolling a repo, revoke access:

```
gh api "orgs/$ORG/actions/secrets/FULLSEND_DISPATCH_TOKEN/repositories/$REPO_ID" --method DELETE --silent
```

The repo ID comes from the `gh api repos/{org}/{repo}` call already made to get the default branch — capture `.id` from the same response.

**Files:** `internal/scaffold/fullsend-repo/scripts/reconcile-repos.sh`

### 3. Remove repo access management from the installer

- `DispatchTokenLayer.Install()`: Remove the `SetOrgSecretRepos` call. The installer creates the secret with `visibility: "selected"` and no repos.
- Remove `enrolledRepoIDs` from `DispatchTokenLayer` and its constructor.
- Remove `collectEnrolledRepoIDs` from `admin.go` and the plumbing that passes repo IDs through `buildLayerStack`.

**Files:** `internal/layers/dispatch.go`, `internal/layers/dispatch_test.go`, `internal/cli/admin.go`

### 4. Update tests

- Update `dispatch_test.go` to remove repo ID assertions from install tests.
- Update any `admin.go` tests that exercise `collectEnrolledRepoIDs` or `buildLayerStack` with repo IDs.

### Edge case: first install

The installer creates the secret with no repos. It then dispatches `repo-maintenance.yml`, which enrolls repos and grants them secret access. The shim workflow works as soon as the enrollment PR merges.

### What stays the same

- `SetOrgSecretRepos` remains in the forge interface (useful for other callers).
- The reconcile script's existing enrollment/unenrollment PR logic is unchanged.
- `CreateOrgSecret` still uses `visibility: "selected"`.
