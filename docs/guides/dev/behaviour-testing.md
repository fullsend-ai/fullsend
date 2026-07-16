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

```
e2e/behaviour/
  features/          # Portable Gherkin scenarios
  fixtures/          # Static content for write_fixture ops
  steps/             # Step definitions
  world/             # Scenario state
  drivers/           # SCM, CI, env interfaces + v1 impls
  suite_test.go      # godog entry (build tag: behaviour)
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
