# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `pkg/behaviourtest/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `pkg/behaviourtest/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `pkg/behaviourtest/drivers/install` | Unified repo allocation: leases pool slots, lazily creates/installs repos, and manages mint lifecycle via `AllocateRepo`/`DeallocateRepo`/`Finalize`/`Capacity` |
| `install.MintDriver` | `pkg/behaviourtest/drivers/install` | Provision and tear down fullsend mint in the acquired pool org (internal to composed `Driver`) |
| `install.State` | `pkg/behaviourtest/drivers/install` | Post-install config paths (script commits, workflow polling) |
| `install.RepoEnsurer` | `pkg/behaviourtest/drivers/install` | Lazily create and install numbered pool repos on demand; caches by org/repo key; concurrent-safe via singleflight (internal to composed `Driver`) |

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

The suite in `e2e/behaviour/suite_test.go` (or an external runner) acquires a pool org via `pkg/e2etest`, runs pre-install cleanup, creates a `MintDriver` (cfmint or legacy), and passes it to `install.NewComposedDriver` which performs suite setup (mint deploy), creates an internal repo-name pool and `RepoEnsurer`, and returns a unified `install.Driver`. The suite then constructs SCM and CI drivers and runs godog with `pkg/behaviourtest/suite.InitScenario`. `InitScenario` clones a template `*world.World` per scenario; repo allocation (slot lease + lazy create/install) is handled by the unified driver's `AllocateRepo` when the `Given the enrolled test repository` step runs. Unsupported `BEHAVIOUR_INSTALL_MODE` values fail at suite startup.

### Install driver (v1 per-repo)

The unified `install.Driver` (created by `install.NewComposedDriver`) wraps a `MintDriver` with an internal pool and `RepoEnsurer`. The `MintDriver` only manages the **mint lifecycle**: the cfmint driver deploys a Cloudflare Worker preview mint and tears it down; the legacy driver holds a pre-configured mint URL. Neither mint driver runs `github setup`, post-install validation, or `TeardownPerRepoInstall` on any target repository — that responsibility belongs to the internal `RepoEnsurer`, which lazily creates and installs numbered pool repos (`test-repo-01` … `test-repo-12`) on demand via `AllocateRepo`.

Pool orgs must already have shared GitHub Apps, org-level mint enrollment, and per-repo mint enrollment for each numbered repo (one-time GCP admin step on the hosted mint project). The driver does not run `fullsend admin install` or `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

Teardown (cfmint) abandons the preview alias via `fullsend mint delete --platform=cloudflare`. The legacy driver's teardown is a no-op.

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

Steps use `world.Install` for config repo paths (`ConfigOwner`, `ConfigRepo`, `ConfigPathPrefix`) instead of hardcoding the per-org `.fullsend` config repo.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, halfsend org pool).

## Future backends checklist

- [ ] GitLab SCM driver + `@skip:gitlab` tag removal
- [ ] Tekton or GitLab CI driver
- [ ] Per-org install driver (`BEHAVIOUR_INSTALL_MODE=per-org`)
- [ ] Non-GitHub install backends
