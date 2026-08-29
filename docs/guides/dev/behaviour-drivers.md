# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `pkg/behaviourtest/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `pkg/behaviourtest/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `pkg/behaviourtest/drivers/install` | Unified surface: repo allocation/deallocation, mint lifecycle, and suite teardown |
| `install.Factory` | `pkg/behaviourtest/drivers/install` | Constructs a unified `Driver` for a given org; takes runtime dependencies (forge client, token, binary, GCP project, logger) as parameters |

v1 reference implementations:

- `pkg/behaviourtest/drivers/scm/github/`
- `pkg/behaviourtest/drivers/scm/gitlab/`
- `pkg/behaviourtest/drivers/ci/githubactions/`
- `pkg/behaviourtest/drivers/install/repopool_cfmint_previews.go` (RepoPoolCFMintPreviews)
- `pkg/behaviourtest/drivers/install/repopool_external_mint.go` (RepoPoolExternalMint)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # also: gitlab; future: forgejo
BEHAVIOUR_CI=githubactions        # future: tekton, gitlabci
BEHAVIOUR_INSTALL_MODE=per-repo   # v1 default and only supported value
ENVIRONMENT=dev                   # mint/infra target: dev (default) or stage
```

The suite in `e2e/behaviour/suite_test.go` (or an external runner) acquires a pool org via `pkg/e2etest`, runs pre-install cleanup, calls an `install.Factory` (e.g. `install.NewRepoPoolCFMintPreviews(...)`) to get a unified `install.Driver` that owns mint deploy, pool allocation, repo ensure, and teardown. The suite constructs SCM and CI drivers, then runs godog with `pkg/behaviourtest/suite.InitScenario`. `InitScenario` clones a template `*world.World` per scenario. When a scenario calls "Given the enrolled test repository", `Driver.AllocateRepo` leases a unique repo name and ensures it is created and installed. `Driver.DeallocateRepo` returns the name in the After hook. `Driver.Finalize` tears down suite-scoped resources (e.g. preview mint) and reclaims outstanding leases. Unsupported `BEHAVIOUR_INSTALL_MODE` or `ENVIRONMENT` values fail at suite startup. `ENVIRONMENT` is `dev` or `stage` (empty defaults to `dev`).

### Install driver (unified)

The suite uses a single unified `install.Driver` constructed via `install.Factory` (e.g. `install.NewRepoPoolCFMintPreviews` or `install.NewRepoPoolExternalMint`). Each concrete driver owns the full lifecycle:

1. Deploys the mint (RepoPoolCFMintPreviews: CF Worker preview; RepoPoolExternalMint: pre-configured URL).
2. Manages an internal channel-based pool of repo names (`test-repo-01` … `test-repo-12`).
3. Lazily creates and installs numbered pool repos on demand via an internal ensurer (concurrent-safe via singleflight).
4. Exposes `AllocateRepo` / `DeallocateRepo` / `Finalize` / `Capacity`.

The Factory takes the allocated org name plus runtime dependencies (forge client, token, CLI binary, GCP project, logger). Driver-specific inputs (PEMs, allowlists, pool size, mint URL) come from env or are computed inside the driver. The suite does not construct or thread pool, ensurer, or mint driver types directly — all internal lifecycle is encapsulated inside the concrete driver returned by the factory. Default concurrency is `driver.Capacity()`; `GODOG_CONCURRENCY` overrides it (warn, do not fail, if concurrency > Capacity).

Pool orgs must already have shared GitHub Apps, org-level mint enrollment, and per-repo mint enrollment for each numbered repo (one-time GCP admin step on the hosted mint project). The driver does not run `fullsend admin install` or `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

`Finalize` (RepoPoolCFMintPreviews) abandons the preview alias via `fullsend mint delete --platform=cloudflare` and reclaims any outstanding leases with an error. The RepoPoolExternalMint driver's teardown is a no-op.

## Adding an SCM driver

1. Implement `scm.Driver` in `pkg/behaviourtest/drivers/scm/<vendor>/`.
2. Register the driver in the suite runner when `BEHAVIOUR_SCM=<vendor>`.
3. Document the env var value here.
4. Add `@skip:<vendor>` tags on scenarios that cannot run until the driver is complete.

Use `forge.Client` for operations it already exposes; add REST helpers inside the driver package only when necessary (e.g. `GetIssue` with labels).

## Adding a CI driver

1. Implement `ci.Driver` — `WaitForWorkflow`, `FindCompletedWorkflowRun`, `AssertNoWorkflow`, `GetRunLogs`, `DownloadArtifacts`, `DownloadNamedArtifactFromRun`, `DownloadNamedArtifactAfter`, `WaitForHarnessAgent`, `WaitForFailedHarnessAgent`, `AssertNoHarnessAgentArtifact`, `CountHarnessDispatches`.
2. Map forge `WorkflowRun` types to portable polling logic; reuse patterns from `e2e/admin/admin_test.go`.
3. Register in suite init for the matching `BEHAVIOUR_CI` value.

## Step definitions

Steps must **not** import forge-specific packages (`internal/forge/github`, `internal/forge/gitlab`) directly — only drivers. This keeps scenarios vendor-agnostic.

Steps use `w.Org` and `w.RepoName` (the allocated repo name) plus per-repo constants from the `install` package (`PerRepoTriageWorkflow`, `PerRepoAgentWorkflow`, `PerRepoAgentArtifact`) for workflow and artifact paths.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, halfsend org pool).

## Future backends checklist

- [x] GitLab SCM driver (implemented; `@skip:gitlab` tag removal pending)
- [ ] Tekton or GitLab CI driver
- [ ] Non-GitHub install backends
