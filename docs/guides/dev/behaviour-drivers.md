# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `pkg/behaviourtest/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `pkg/behaviourtest/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `pkg/behaviourtest/drivers/install` | Provision and tear down fullsend in the acquired pool org |
| `install.State` | `pkg/behaviourtest/drivers/install` | Post-install config paths (script commits, workflow polling) |

v1 reference implementations:

- `pkg/behaviourtest/drivers/scm/github/`
- `pkg/behaviourtest/drivers/ci/githubactions/`
- `pkg/behaviourtest/drivers/install/perrepo_github.go` (`BEHAVIOUR_INSTALL_MODE=per-repo`)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # future: gitlab, forgejo
BEHAVIOUR_CI=githubactions        # future: tekton, gitlabci
BEHAVIOUR_INSTALL_MODE=per-repo   # v1 default and only supported value
```

The suite in `e2e/behaviour/suite_test.go` (or an external runner) acquires a pool org via `pkg/e2etest`, runs pre-install cleanup, calls `install.Driver.Install`, constructs SCM and CI drivers, creates a `world.RepoPool` (a buffered-channel lease pool of logical repo names), then runs godog with `pkg/behaviourtest/suite.InitScenario`. `InitScenario` clones a template `*world.World` per scenario and leases a unique repo name from the pool for the scenario's duration. Unsupported `BEHAVIOUR_INSTALL_MODE` values fail at suite startup.

### Install driver (v1 per-repo)

Uses `fullsend inference provision <org>/test-repo` then `fullsend github setup <org>/test-repo --vendor --direct --skip-app-setup --runtime dummy` with the repo-scoped WIF provider from provision (`E2E_GCP_PROJECT_ID`). Pool orgs must already have shared GitHub Apps, org-level mint enrollment, and per-repo mint enrollment for `test-repo` (one-time GCP admin step on the hosted mint project). The driver does not run `fullsend admin install` or `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

Teardown removes shim workflows, stale branches, and open fullsend PRs on `test-repo` via `pkg/e2etest.TeardownPerRepoInstall`.

## Adding an SCM driver

1. Implement `scm.Driver` in `pkg/behaviourtest/drivers/scm/<vendor>/`.
2. Register the driver in the suite runner when `BEHAVIOUR_SCM=<vendor>`.
3. Document the env var value here.
4. Add `@skip:<vendor>` tags on scenarios that cannot run until the driver is complete.

Use `forge.Client` for operations it already exposes; add REST helpers inside the driver package only when necessary (e.g. `GetIssue` with labels).

## Adding a CI driver

1. Implement `ci.Driver` — `WaitForWorkflow`, `FindCompletedWorkflowRun`, `AssertNoWorkflow`, `GetRunLogs`, `DownloadArtifacts`, `DownloadNamedArtifactFromRun`, `DownloadNamedArtifactAfter`, `WaitForHarnessAgent`, `AssertNoHarnessAgentArtifact`, `CountHarnessDispatches`.
2. Map forge `WorkflowRun` types to portable polling logic; reuse patterns from `e2e/admin/admin_test.go`.
3. Register in suite init for the matching `BEHAVIOUR_CI` value.

## Step definitions

Steps must **not** import forge-specific packages (`internal/forge/github`, `internal/forge/gitlab`) directly — only drivers. This keeps scenarios vendor-agnostic.

Steps use `world.Install` for config repo paths (`ConfigOwner`, `ConfigRepo`, `ConfigPathPrefix`) instead of hardcoding the per-org `.fullsend` config repo.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, halfsend org pool).

## Future backends checklist

- [ ] GitLab SCM driver + `@skip:gitlab` tag removal
- [ ] Tekton or GitLab CI driver
- [ ] Per-org install driver (`BEHAVIOUR_INSTALL_MODE=per-org`)
- [ ] Non-GitHub install backends
