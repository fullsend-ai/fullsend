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
| `op` | `read_file`, `url_get`, `run_command`, `write_fixture` |
| `args` | Op-specific; see below |

**`write_fixture`:** `dest_path, fixtures/...` — content lives in `e2e/behaviour/fixtures/`, embedded in the committed scenario script at `.fullsend/behaviour/current-scenario.yaml`.

### Assertion steps

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
export GITHUB_TOKEN=...   # PAT with access to halfsend org pool
make behaviour-test
```

Test orgs (`halfsend-01` … `halfsend-06`) must have per-org fullsend installed with `--runtime dummy` and `test-repo` enrolled.

Runner env (defaults shown):

```
BEHAVIOUR_SCM=github
BEHAVIOUR_CI=githubactions
BEHAVIOUR_INSTALL_MODE=per-org
```

See [behaviour-drivers.md](behaviour-drivers.md) for driver configuration and [ADR 0043](../../ADRs/0043-behaviour-tests-with-gherkin-and-drivers.md) for the decision record.
