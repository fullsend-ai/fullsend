# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `pkg/behaviourtest/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `pkg/behaviourtest/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `pkg/behaviourtest/drivers/install` | Unified surface: ephemeral repo creation/deletion, mint lifecycle, and suite teardown |
| `install.Factory` | `pkg/behaviourtest/drivers/install` | Constructs a unified `Driver` for a given org; takes runtime dependencies (forge client, token, binary, GCP project, logger) as parameters |

v1 reference implementations:

- `pkg/behaviourtest/drivers/scm/github/`
- `pkg/behaviourtest/drivers/scm/gitlab/`
- `pkg/behaviourtest/drivers/ci/githubactions/`
- `pkg/behaviourtest/drivers/ci/gitlabci/`
- `pkg/behaviourtest/drivers/install/factory_cfmint.go` (NewCFMintFactory)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # also: gitlab; future: forgejo
BEHAVIOUR_CI=githubactions        # also: gitlabci; future: tekton
BEHAVIOUR_ORG=fullsend-ai-test    # default; override for a different test org
BEHAVIOUR_FULLSEND_REF=           # optional; defaults to PR head SHA or main
ENVIRONMENT=dev                   # mint/infra target: dev (default) or stage
```

The suite in `e2e/behaviour/suite_test.go` (or an external runner) obtains a token for the configured test org (`BEHAVIOUR_ORG`) via `pkg/e2etest`, calls an `install.Factory` (e.g. `install.NewCFMintFactory(...)`) to get a unified `install.Driver` that owns mint deploy, ephemeral repo creation, and teardown. The suite constructs SCM and CI drivers, then runs godog with `pkg/behaviourtest/suite.InitScenario`. `InitScenario` clones a template `*world.World` per scenario. When a scenario calls "Given the installed test repository", `Driver.CreateRepo` creates an ephemeral `bt-{randHex}-{hint}` repo and installs fullsend. `Driver.DeleteRepo` cleans up the repo in the After hook. `Driver.Finalize` tears down suite-scoped resources (e.g. preview mint) and cleans up any outstanding repos. `ENVIRONMENT` is `dev` or `stage` (empty defaults to `dev`).

### Install driver (unified)

The suite uses a single unified `install.Driver` constructed via `install.Factory` (e.g. `install.NewCFMintFactory`). The concrete driver owns the full lifecycle:

1. Deploys a preview mint (CF Worker preview).
2. Creates ephemeral `bt-{randHex}-{hint}` repos on demand via an internal ensurer (concurrent-safe).
3. Installs fullsend via `repos install --fullsend-ref` and polls for git/Actions readiness.
4. Exposes `CreateRepo` / `DeleteRepo` / `Finalize`.

The Factory takes the configured org name plus runtime dependencies (forge client, token, CLI binary, GCP project, logger). Driver-specific inputs (PEMs, allowlists, mint URL) come from env or are computed inside the driver. The suite does not construct or thread ensurer or mint driver types directly — all internal lifecycle is encapsulated inside the concrete driver returned by the factory.

The test org must have shared GitHub Apps, org-level mint enrollment, and wildcard per-repo mint enrollment (`PER_REPO_WIF_REPOS=*`). The driver does not run `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

`Finalize` abandons the preview alias via `fullsend mint delete --platform=cloudflare` and cleans up any outstanding repos with an error.

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

Steps use `w.Org` and `w.RepoName` (the created repo name) plus constants from the `install` package (`TriageWorkflow`, `AgentWorkflow`, `AgentArtifact`) for workflow and artifact paths.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, test org).

## Future backends checklist

- [x] GitLab SCM driver (implemented; `@skip:gitlab` tag removal pending)
- [x] GitLab CI driver (implemented; suite wiring currently uses a GitHub-backed `forge.Client` — not yet live-testable against real GitLab backends)
- [ ] Tekton CI driver
- [ ] Non-GitHub install backends
