# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `e2e/behaviour/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `e2e/behaviour/drivers/ci` | Workflow polling, logs, artifact download |
| `env.Setup` | `e2e/behaviour/drivers/env` | Validate org pool org has per-org install + enrolled test repo |

v1 reference implementations:

- `e2e/behaviour/drivers/scm/github/`
- `e2e/behaviour/drivers/ci/githubactions/`
- `e2e/behaviour/drivers/env/` (`PerOrg`)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # future: gitlab, forgejo
BEHAVIOUR_CI=githubactions        # future: tekton, gitlabci
BEHAVIOUR_INSTALL_MODE=per-org    # v1 default and only supported value
```

The suite in `e2e/behaviour/suite_test.go` reads these env vars, validates them, and constructs concrete drivers.

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

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends mirror admin e2e credentials (`GITHUB_TOKEN`, halfsend org pool).

## Future backends checklist

- [ ] GitLab SCM driver + `@skip:gitlab` tag removal
- [ ] Tekton or GitLab CI driver
- [ ] Per-repo install mode matrix + `@requires:per-repo` scenarios
