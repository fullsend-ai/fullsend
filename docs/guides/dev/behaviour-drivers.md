# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `e2e/behaviour/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `e2e/behaviour/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `e2e/behaviour/drivers/install` | Provision and tear down fullsend in the acquired pool org |
| `install.State` | `e2e/behaviour/drivers/install` | Post-install config paths (script commits, workflow polling) |

v1 reference implementations:

- `e2e/behaviour/drivers/scm/github/`
- `e2e/behaviour/drivers/ci/githubactions/`
- `e2e/behaviour/drivers/install/github/perrepo/` (`BEHAVIOUR_INSTALL_MODE=per-repo`)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # future: gitlab, forgejo
BEHAVIOUR_CI=githubactions        # future: tekton, gitlabci
BEHAVIOUR_INSTALL_MODE=per-repo   # v1 default and only supported value
```

The suite in `e2e/behaviour/suite_test.go` acquires a pool org, runs pre-install cleanup, calls `install.Driver.Install`, then constructs SCM and CI drivers. Unsupported `BEHAVIOUR_INSTALL_MODE` values fail at suite startup.

### Install driver (v1 per-repo)

Uses `fullsend github setup <org>/test-repo --vendor --direct --skip-app-setup --runtime dummy` with inference flags from `E2E_GCP_PROJECT_ID` and `E2E_GCP_WIF_PROVIDER`. When `E2E_GCP_PROJECT_ID` is set (CI), the driver then runs `fullsend mint enroll <org>/test-repo` so vendored workflows can mint same-org triage tokens. Pool orgs must already have shared GitHub Apps and org-level mint enrollment; the driver does not run `fullsend admin install`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment) for CI service-account IAM on the mint project.

Teardown removes shim workflows, stale branches, and open fullsend PRs on `test-repo` via shared helpers in `e2e/admin/cleanup.go`.

## Adding an SCM driver

1. Implement `scm.Driver` in `e2e/behaviour/drivers/scm/<vendor>/`.
2. Register the driver in `suite_test.go` when `BEHAVIOUR_SCM=<vendor>`.
3. Document the env var value here.
4. Add `@skip:<vendor>` tags on scenarios that cannot run until the driver is complete.

Use `forge.Client` for operations it already exposes; add REST helpers inside the driver package only when necessary (e.g. `GetIssue` with labels).

## Adding a CI driver

1. Implement `ci.Driver` — `WaitForWorkflow`, `AssertNoWorkflow`, `GetRunLogs`, `DownloadArtifacts`.
2. Map forge `WorkflowRun` types to portable polling logic; reuse patterns from `e2e/admin/admin_test.go`.
3. Register in suite init for the matching `BEHAVIOUR_CI` value.

## Step definitions

Steps must **not** import `internal/forge/github` directly — only drivers. This keeps scenarios vendor-agnostic.

Steps use `world.Install` for config repo paths (`ConfigOwner`, `ConfigRepo`, `ConfigPathPrefix`) instead of hardcoding the per-org `.fullsend` config repo.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, halfsend org pool).

## Future backends checklist

- [ ] GitLab SCM driver + `@skip:gitlab` tag removal
- [ ] Tekton or GitLab CI driver
- [ ] Per-org install driver (`BEHAVIOUR_INSTALL_MODE=per-org`)
- [ ] Non-GitHub install backends
