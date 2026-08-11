# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `pkg/behaviourtest/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `pkg/behaviourtest/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `pkg/behaviourtest/drivers/install` | Provision and tear down fullsend in the acquired pool org |
| `install.State` | `pkg/behaviourtest/drivers/install` | Post-install config paths (script commits, workflow polling) |
| `install.RepoEnsurer` | `pkg/behaviourtest/drivers/install` | Lazily create and install numbered pool repos on demand; caches by org/repo key; concurrent-safe via singleflight |

v1 reference implementations:

- `pkg/behaviourtest/drivers/scm/github/`
- `pkg/behaviourtest/drivers/ci/githubactions/`
- `pkg/behaviourtest/drivers/install/legacy/driver.go` (`BEHAVIOUR_INSTALL_MODE=per-repo`, legacy path)
- `pkg/behaviourtest/drivers/install/cfmint/driver.go` (`BEHAVIOUR_INSTALL_MODE=per-repo`, CF Worker preview mint)
- `pkg/behaviourtest/drivers/install/common/setup.go` (shared helpers: `RunGitHubSetup`, `ProvisionInference`)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # future: gitlab, forgejo
BEHAVIOUR_CI=githubactions        # future: tekton, gitlabci
BEHAVIOUR_INSTALL_MODE=per-repo   # v1 default and only supported value
```

The suite in `e2e/behaviour/suite_test.go` (or an external runner) acquires a pool org via `pkg/e2etest`, runs pre-install cleanup, calls `install.Driver.Install`, constructs SCM and CI drivers, creates a `world.RepoPool` (a buffered-channel lease pool of logical repo names), then runs godog with `pkg/behaviourtest/suite.InitScenario`. `InitScenario` clones a template `*world.World` per scenario and leases a unique repo name from the pool for the scenario's duration. Unsupported `BEHAVIOUR_INSTALL_MODE` values fail at suite startup.

### Install driver (v1 per-repo)

Uses `fullsend inference provision <org>/test-repo` then `fullsend github setup <org>/test-repo --vendor --direct --skip-app-setup --runtime dummy` with the repo-scoped WIF provider from provision (`E2E_GCP_PROJECT_ID`). Pool orgs must already have shared GitHub Apps, org-level mint enrollment, and per-repo mint enrollment for `test-repo` (one-time GCP admin step on the hosted mint project). Numbered `test-repo-01` … `test-repo-12` are enrolled and actively leased from `world.RepoPool` during parallel scenario execution (see `GODOG_CONCURRENCY` in [behaviour-testing.md](behaviour-testing.md#parallel-execution)). The driver does not run `fullsend admin install` or `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

Teardown removes shim workflows, stale branches, and open fullsend PRs on `test-repo` via `pkg/e2etest.TeardownPerRepoInstall`.

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

## Adding an install driver

1. **Discover existing env vars.** Read `.github/workflows/e2e.yml` for
   secrets and env vars already wired into the BT job (search for `env:`
   blocks in the behaviour test step). Use existing vars — e.g.,
   `TEST_*_PEM` for role PEMs, `TEST_CLOUDFLARE_*` for CF credentials —
   rather than inventing new ones. Cross-reference
   [e2e-testing.md](e2e-testing.md#test-github-apps) for the full
   secrets inventory and app-to-PEM mapping.
2. **Discover CLI flag surface.** Read the CLI source for the commands
   your driver will invoke (e.g., `internal/cli/mint.go` for
   `mint deploy`, `internal/cli/mint_delete.go` for `mint delete`).
   Check the full flag surface — especially optional flags like
   `--worker-name`, `--allowed-orgs`, `--workflow-host-repos`,
   `--per-repo-wif-repos`, `--app-set`, and `--pem-dir`. Ensure deploy
   and teardown commands receive symmetric identifying flags (e.g., both
   `mint deploy` and `mint delete` need `--worker-name` if the Worker
   name is non-default).
3. **Handle app set.** Test PEMs belong to the `fullsend-test` app set,
   not the default `fullsend-ai`. Pass `--app-set fullsend-test`
   explicitly when deploying with test PEMs. Omitting this causes the
   CLI to validate PEMs against the wrong GitHub Apps.
4. **Implement `install.Driver`** in a new sub-package under
   `pkg/behaviourtest/drivers/install/<variant>/`. Each driver variant
   gets its own package (e.g., `install/cfmint/`, `install/legacy/`)
   behind the shared `install.Driver` interface (`Install` and
   `Teardown` methods). Place common helpers shared across drivers in
   `install/common/` (e.g., `RunGitHubSetup`, `ProvisionInference`).
5. **Register the driver** in the suite init for the matching
   `BEHAVIOUR_INSTALL_MODE` value (or a new mode selector).
6. **Use `install/legacy/driver.go`** (~67 lines) as the minimal
   reference implementation. For a more complex example showing CLI arg
   construction, preview alias generation, and teardown semantics, see
   `install/cfmint/driver.go`.
7. **Export CLI arg builders.** Export functions like `DeployArgs` and
   `TeardownArgs` so unit tests can verify arg construction without
   shelling out to the real CLI.
8. **Document the new driver** here — add the env var value and any new
   secrets or configuration required.

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
