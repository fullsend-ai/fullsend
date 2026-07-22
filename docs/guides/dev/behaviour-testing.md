# Behaviour testing

End-to-end Gherkin tests under `e2e/behaviour/` validate **deterministic platform code** with inference removed. They are **orthogonal** to LLM and instruction testing in [testing-agents.md](../../problems/testing-agents.md) and to admin install e2e in `e2e/admin/`.

| | Behaviour tests | LLM evals | Admin e2e | Unit tests |
|---|-----------------|-----------|-----------|------------|
| **Target** | Platform workflows, sandbox, SCM | Prompts, models | Install/uninstall | Go functions |
| **Inference** | Dummy runtime | Real LLM | Real LLM | N/A |
| **Infrastructure** | Live GitHub + GHA | Varies | Live GitHub + GHA | None |

## When to add a behaviour test

Add one when a **user-visible workflow** must be verified end-to-end (dispatch → workflow → post-script → SCM state) and the assertion is **binary**. Prefer unit tests for pure Go logic and admin e2e for install provisioning.

## Layout

Shared framework (importable by external repos):

```
pkg/behaviourtest/
  world/             # Scenario state
  steps/             # Step definitions + CleanupScenario
  artifacts/         # Artifact lookup helpers
  drivers/           # SCM, CI, env, install interfaces + v1 impls
  suite/             # InitScenario (tags, hooks, step registration)
pkg/e2etest/         # Org pool, CLI runner, cleanup (shared with admin e2e)
```

In-repo runner and scenarios:

```
e2e/behaviour/
  features/          # Portable Gherkin scenarios
  fixtures/          # Static content for write_fixture ops
  suite_test.go      # Thin godog entry (build tag: behaviour)
```

## Writing scenarios

Describe **user-visible behaviour** only. Do not encode SCM vendor, CI platform, or install mode in feature files.

### Dummy agent tables

```gherkin
Given a dummy agent that would:
  | description      | op            | args                                                      |
  | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
```

| Column | Meaning |
|--------|---------|
| `description` | Human label matched by assertion steps |
| `op` | `read_file`, `url_get`, `write_fixture` |
| `args` | Op-specific; see below |

**`write_fixture`:** `dest_path, fixtures/...` — content lives in `e2e/behaviour/fixtures/`, embedded in the committed scenario script at `.fullsend/behaviour/current-scenario.yaml`.

### Assertion steps

Each assertion verifies immediately against workflow artifacts. If the triage workflow has not been waited on yet, the step waits for completion and downloads artifacts first (same as `Then the triage workflow completes successfully`).

```gherkin
Then the agent will succeed to Emit triage JSON
And the agent will fail to Search for foo
And the agent will output issues.out with:
  """
  expected content
  """
```

### Compatibility tags

Use tags only for **exceptions** when a backend cannot run a scenario yet: `@skip:gitlab`, `@skip:per-org`, `@requires:per-repo`. Untagged scenarios run everywhere applicable.

## Running locally

```bash
# Local: gh auth login or export GH_TOKEN/GITHUB_TOKEN with access to halfsend org pool
make behaviour-test
```

In CI, the test runner mints cross-org `e2e` installation tokens via OIDC (same as admin e2e) for GitHub API operations. Triage workflows on the pool org's `test-repo` mint same-org `triage` tokens from vendored reusable workflows; those require per-repo mint enrollment (`PER_REPO_WIF_REPOS`) on the hosted mint project. Pool `test-repo` repos are enrolled once by a GCP admin — not during CI install. The install driver provisions repo-scoped inference WIF via `fullsend inference provision` before `github setup`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

Runner env (defaults shown):

```
BEHAVIOUR_SCM=github
BEHAVIOUR_CI=githubactions
BEHAVIOUR_INSTALL_MODE=per-repo
E2E_GCP_PROJECT_ID=...        # inference project; install runs inference provision per pool repo
E2E_GCP_WIF_PROVIDER=...      # CI job GCP auth (not written to pool test-repo secrets)
```

Triage scenarios apply the `ready-for-triage` label (not `/fs-triage` comments) because the per-repo shim ignores `issue_comment` events from bot users and CI uses minted e2e installation tokens.

For the reusable test GitHub Apps (`fullsend-test-*`) used by temporary and test mints, see [Test GitHub Apps](e2e-testing.md#test-github-apps) in the e2e testing guide.

See [behaviour-drivers.md](behaviour-drivers.md) for driver configuration and [ADR 0066](../../ADRs/0066-behaviour-tests-with-gherkin-and-drivers.md) for the decision record.

## Fork PR scenarios

Fork dispatch scenarios test `pull_request_target` harness triggering from cross-fork pull requests.

### Pool-org prerequisites

Fork scenarios require the pool org to have:

- **A long-lived fork repository** of the enrolled `test-repo`. The fork is created once (idempotently) via the `Given a fork` step and persists across test runs. Do not delete the fork repo between scenarios or CI runs.
- **The same installation token** must have write access to both the base repo and the fork repo within the org, since the e2e bot commits to the fork and opens cross-fork PRs.

### Fork lifecycle

| Resource | Lifecycle | Cleanup |
|----------|-----------|---------|
| Fork repo | Long-lived (created once per pool org) | Never deleted |
| Fork branches | Per-scenario | Deleted by `CleanupScenario` |
| Fork PRs | Per-scenario | Closed by `CleanupScenario` |

Fork PRs are opened against the base repo (not the fork). `CleanupScenario` closes them via `CloseIssue` on the base repo and deletes the head branch on the fork repo.

### Background step usage

Fork scenarios share a common `Background:` block that sets up the enrolled test repository and the fork:

```gherkin
Background:
  Given the enrolled test repository
  And a fork "test-repo-fork" of the enrolled test repository
```

The `Given a fork` step is idempotent: if the fork already exists, it reuses it without error. Each scenario then creates its own branch and PR within the fork.

## Version pinning for `fullsend-ai/agents`

External behaviour runners import the shared libraries from this module:

```go
require github.com/fullsend-ai/fullsend v0.x.y // released tag, not @main
```

- Import `github.com/fullsend-ai/fullsend/pkg/behaviourtest/...` for world, steps, drivers, and `suite.InitScenario`.
- Import `github.com/fullsend-ai/fullsend/pkg/e2etest` for org pool acquisition, env config, CLI build/run, and cleanup.
- Set `world.FixturesRoot` to the module-relative fixtures directory (e.g. `"behaviour"` in the agents repo).
- Build the fullsend CLI with `e2etest.BuildModuleBinary(t, "github.com/fullsend-ai/fullsend")` — not `BuildCLIBinary`, which resolves the **current** module root.
- Run with `-tags behaviour` and the same env vars as CI (see above).

### API changes

**`suite.InitScenario` signature change (v0.22+):** The function signature changed from `InitScenario(sc, w)` to `InitScenario(sc, template, pool)`. Instead of passing a single `*world.World`, callers now pass a template `*world.World` (cloned per scenario) and a `*world.RepoPool` (used to lease unique repo names). Update your `suite_test.go` accordingly:

```go
pool, err := world.NewRepoPool(12)
if err != nil {
    t.Fatalf("creating repo pool: %v", err)
}

template := &world.World{ /* ... driver fields ... */ }

suiteRunner := godog.TestSuite{
    ScenarioInitializer: func(sc *godog.ScenarioContext) {
        suite.InitScenario(sc, template, pool)
    },
    // ...
}
```

**`steps.Register` signature change (v0.22+):** The function signature changed from `Register(ctx, w)` (where `ctx` was a `*godog.ScenarioContext` and `w` was a `*world.World`) to `Register(sc)`. Step definitions no longer receive `*world.World` as a parameter. Instead, they accept `context.Context` and extract the per-scenario World via `world.FromContext(ctx)`.

Bump the pinned version when behaviour step vocabulary or `pkg/e2etest` / `pkg/behaviourtest` APIs change.
