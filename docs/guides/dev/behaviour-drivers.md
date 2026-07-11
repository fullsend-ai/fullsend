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
- `e2e/behaviour/drivers/install/perrepo_github.go` (`BEHAVIOUR_INSTALL_MODE=per-repo`)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # future: gitlab, forgejo
BEHAVIOUR_CI=githubactions        # future: tekton, gitlabci
BEHAVIOUR_INSTALL_MODE=per-repo   # v1 default and only supported value
```

The suite in `e2e/behaviour/suite_test.go` acquires a pool org, runs pre-install cleanup, calls `install.Driver.Install`, then constructs SCM and CI drivers. Unsupported `BEHAVIOUR_INSTALL_MODE` values fail at suite startup.

### Install driver (v1 per-repo)

Uses `fullsend inference provision <org>/test-repo` then `fullsend github setup <org>/test-repo --vendor --direct --skip-app-setup --runtime dummy` with the repo-scoped WIF provider from provision (`E2E_GCP_PROJECT_ID`). Pool orgs must already have shared GitHub Apps, org-level mint enrollment, and per-repo mint enrollment for `test-repo` (one-time GCP admin step on the hosted mint project). The driver does not run `fullsend admin install` or `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

Teardown removes shim workflows, stale branches, and open fullsend PRs on `test-repo` via shared helpers in `e2e/admin/cleanup.go`.

## Adding an SCM driver

1. Implement `scm.Driver` in `e2e/behaviour/drivers/scm/<vendor>/`.
2. Register the driver in `suite_test.go` when `BEHAVIOUR_SCM=<vendor>`.
3. Document the env var value here.
4. Add `@skip:<vendor>` tags on scenarios that cannot run until the driver is complete.

Use `forge.Client` for operations it already exposes; add REST helpers inside the driver package only when necessary (e.g. `GetIssue` with labels).

## Local emulator (`scm/emulate`)

`e2e/behaviour/drivers/scm/emulate/` implements `scm.Driver` against a locally spawned
[vercel-labs/emulate](https://github.com/vercel-labs/emulate) GitHub instance instead of live
GitHub — no pool org, no mint, no secrets, sub-second per test. It reuses `scm/github`'s driver
unmodified: `emulate.Start` just points `forge/github.LiveClient` at the emulator's localhost
URL via `WithBaseURL` and wraps it with `github.New`.

This package deliberately does **not** follow the "Adding an SCM driver" checklist above — it
is not registered as a `BEHAVIOUR_SCM` value and is not wired into `suite_test.go`. emulate's
Actions endpoints are REST record-level only (list/get/dispatch/cancel/logs as data); it does
not execute real workflow YAML. So it can never satisfy `ci.Driver` for scenarios like
`triage.feature` that assert on real agent execution — pairing it with `BEHAVIOUR_CI=githubactions`
would silently produce a suite that can never observe what it's supposed to test. Import it
directly in Go test code that only needs SCM-level state (issues, labels, comments) and no
live Actions run — e.g. a future dispatch-routing test, or `eval/`-layer fixture setup.

There is no reset endpoint: `reset()` in emulate is reachable only through its programmatic
`createEmulator()` API, not the CLI-spawned server this package shells out to. Start one
`Instance` per test binary (see `emulate_test.go`'s `TestMain`) and let each test create its
own issue — `CreateIssue` returns a fresh, unique number every call, so tests don't collide.

## Adding a CI driver

1. Implement `ci.Driver` — `WaitForWorkflow`, `AssertNoWorkflow`, `GetRunLogs`, `DownloadArtifacts`.
2. Map forge `WorkflowRun` types to portable polling logic; reuse patterns from `e2e/admin/admin_test.go`.
3. Register in suite init for the matching `BEHAVIOUR_CI` value.

## Step definitions

Steps must **not** import `internal/forge/github` directly — only drivers. This keeps scenarios vendor-agnostic.

Steps use `world.Install` for config repo paths (`ConfigOwner`, `ConfigRepo`, `ConfigPathPrefix`) instead of hardcoding the per-org `.fullsend` config repo.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, halfsend org pool). For scenarios that only need SCM-level state and no live Actions run, prefer `scm/emulate` (above) over `httptest` or live credentials — real multi-endpoint behavior (create → label → read-back) without hand-stubbing each response.

## Future backends checklist

- [ ] GitLab SCM driver + `@skip:gitlab` tag removal
- [ ] Tekton or GitLab CI driver
- [ ] Per-org install driver (`BEHAVIOUR_INSTALL_MODE=per-org`)
- [ ] Non-GitHub install backends
