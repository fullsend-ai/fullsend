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
| `op` | `read_file`, `url_get`, `write_fixture`, `assert_env`, `assert_file`, `assert_json`, `checkout_branch` |
| `args` | Op-specific; see below |

**`write_fixture`:** `dest_path, fixtures/...` — content lives in `e2e/behaviour/fixtures/`, embedded in the committed scenario script at `.fullsend/behaviour/current-scenario.yaml`.

**`checkout_branch`:** a single regex-validated branch name. Probes the remote with `git ls-remote --exit-code`; when the ref exists it is fetched and the branch is based on `FETCH_HEAD` (so the branch carries that ref's commits), when the ref is absent the branch is based on the current `HEAD`, and any other probe failure (network, auth) fails the op instead of silently falling back. The op then records one marker commit on the branch so the applier post-script has content to push — and so a wrongful push moves the target branch tip, giving `branch ... is unchanged` assertions something to detect. Deliberately a narrow capability — not a general shell op.

The `<issue>` placeholder expands to the scenario's issue number in `checkout_branch` args (only that op) and in the branch step definitions' branch names and head-branch patterns; order the `an issue` step before any step using it.

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

### Runtime steps

Every scenario runs the stage under the dummy runtime selected at install time (`github setup … --runtime dummy`). The runtime layer gets two kinds of coverage without leasing extra repos or adding wall time:

- **Core (every run):** `Then the run selected the "dummy" runtime` reads the `runtime` field the runner writes into `metrics.json`, proving the repo's `.fullsend/config.yaml` `runtime:` reached backend selection. Use it in one representative scenario per stage; the artifact is already downloaded for the other assertions.
- **Runtime-specific (gated):** `Given the repository runtime is "<name>"` commits `runtime: <name>` to the leased repo's config for this scenario only (CleanupScenario restores `dummy` — slots are reused, so never set it any other way; the step refuses if the slot is not on `dummy` to begin with). The custom-harness step commits only a placeholder for a relative `agent:` path, which a real runtime cannot act on, so follow it with `And a pi agent "<name>" defined as:` and a docstring holding the full agent file (frontmatter + body) — `{{fixture:fixtures/<stage>/<file>.json}}` inlines a result fixture so the model has a concrete, deterministic file to write (the custom harness carries no post-script, so nothing validates it; the assertions are on the transcript and metrics). Then the scenario dispatches the harness and asserts on artifacts: `the run selected the "pi" runtime`, `the pi session transcript records at least one tool call` (the agent used a tool through pi; with security enabled the run refuses to start without the intact hook adapter, so the call was mediated by it — the step does not inspect hook output), `the run metrics report tokens`. Such scenarios cost a real model run on the pool repo's repo-scoped Vertex WIF and must be tagged `@requires:capability:runtime-<name>` so they only run where the runner declares the capability; `make behaviour-test` declares `runtime-pi` by default (a `Makefile` variable, so a PR adding a gated scenario exercises it on its own `pull_request_target` run — the workflow file itself comes from `main`); `BEHAVIOUR_CAPABILITIES= make behaviour-test` skips them. See `features/runtime/pi.feature`.
- **Per-agent (every run):** `Given the repository agents are configured with:` with a YAML docstring (`triage:\n  runtime: dummy`) sets runtime/model/effort on the leased repo's `agents:` entries (a name-only entry for a built-in, the sourced entry for a custom agent; only the settings given change) — validated the way `fullsend run` validates them — and CleanupScenario restores the pre-scenario `agents:` list. Pair it with `the repository runtime is "<real runtime>"` and pin every agent the scenario can dispatch (triage hands off to `code` via `ready-to-code`) back to `dummy`, then assert `the run selected the "dummy" runtime from "agents.triage"`, which also checks `runtime_source` in `metrics.json` ends with that entry — proof the per-agent entry decided, at dummy cost. The gated second scenario in the same file leaves the repo on `dummy` and puts one custom agent on pi with `model: haiku` from its entry (the harness says `opus`); `the run requested model "haiku" from "agents.<name>" and the provider reported a "haiku" model` checks `requested_model`, `override_source`, the reported `model` and `num_turns` in `metrics.json`. See `features/runtime/agent-settings.feature`.

Do not add runtime coverage to `e2e/admin` (org-mode install, deprecated per ADR 0044) or behind new `fullsend admin` flags.

### Branch assertion steps

For scenarios that drive a run through the post-scripts to a real push,
`pkg/behaviourtest/steps/branch.go` adds SCM-level assertions. Record a
branch tip before the run to assert it did not move afterwards:

```gherkin
Given an open pull request on branch "agent/990000099-decoy"
And the tip of branch "agent/990000099-decoy" is recorded
...
Then the pull request head branch matches "agent/<issue>-.*"
And branch "agent/990000099-decoy" is unchanged
```

`the pull request head branch matches` asserts exactly **one** open PR
head matches the pattern. The pattern is a Go regular expression,
anchored on both ends by the step — a literal `.` in a branch name must
be escaped, and a pattern that could also match a fixture branch (e.g. a
decoy inside the `agent/` namespace) makes the step fail as ambiguous.

For fail-closed paths there is a failure counterpart, asserted against
the run conclusion plus the post-script's failure comment on the
scenario PR:

```gherkin
Then the harness "fix" workflow fails reporting "Refusing to push"
```

Pick a stable fragment of the failure-comment contract (the category
label headline or a fixed detail phrase). No shipped scenario uses this
step yet — the fix stage's only dispatch route is a `changes_requested`
review from the org review bot, which the suite cannot produce — but the
step is unit-tested and ready for a suite-reachable fail-closed path.

### Compatibility tags

Use tags only for **exceptions** when a backend cannot run a scenario yet: `@skip:gitlab`, `@skip:per-org`, `@requires:per-repo`. Untagged scenarios run everywhere applicable.

`@requires:capability:<name>` gates scenarios that assert behavior only present past a dependency version (e.g. an agents-repo release). Such scenarios are skipped unless the runner declares the capability in the comma-separated `BEHAVIOUR_CAPABILITIES` env var:

```bash
BEHAVIOUR_CAPABILITIES=applier-branch-namespace make behaviour-test
```

This keeps CI green until the dependency ships; flip the capability on (locally or in the CI env) once the pinned dependency includes the behavior.

## Fixture authoring

Every scenario that dispatches an agent stage must include a `write_fixture` row emitting `output/agent-result.json` with content that conforms to the stage's result schema. The harness post-script validates this file before performing any post-processing (labelling, commenting, PR creation). If the fixture is missing or invalid, the harness fails with `Validation failed: FAIL: output/agent-result.json not found`.

### Checklist for new scenarios

Before merging a scenario that dispatches an agent stage:

1. Identify the agent **role** (triage, code, review, fix, retro, prioritize). The role determines which result schema applies.
2. Create a fixture JSON file under `e2e/behaviour/fixtures/<stage>/` that satisfies the corresponding schema at `schemas/<stage>-result.schema.json` (scaffolded into target repos under `internal/scaffold/fullsend-repo/schemas/`).
3. Add a `write_fixture` row to the dummy agent table:
   ```
   | Emit <stage> JSON | write_fixture | output/agent-result.json, fixtures/<stage>/<name>.json |
   ```
4. Add an assertion step to verify the fixture was written:
   ```gherkin
   And the agent will succeed to Emit <stage> JSON
   ```
5. Verify that fixture field values satisfy downstream CLI validation, not just the JSON schema. For example, `head_sha` in `review-result` must be a full-length 40-character hex SHA (`abcdef0123456789abcdef0123456789abcdef01`), not a short prefix.

### Fixture inventory

Existing fixtures under `e2e/behaviour/fixtures/`:

| Fixture | Target schema | Purpose |
|---------|---------------|---------|
| `triage/sufficient.json` | `triage-result.schema.json` | Triage stage result with `action: "sufficient"` |
| `dispatch/ok.json` | _(none — dispatch proof)_ | Lightweight proof-of-execution marker for dispatch scenarios |
| `review/comment.json` | `review-result.schema.json` | Review stage result with `action: "comment"` |
| `code/implemented.json` | `code-result.schema.json` | Code stage result targeting the default branch |

The `dispatch/ok.json` fixture is not emitted as `output/agent-result.json` — it is used for auxiliary proof-of-execution files (e.g., `output/bash-routing-ok.json`). Scenarios that dispatch a **real agent stage** (triage, review, code, fix) must emit a schema-valid fixture to `output/agent-result.json`.

### Adding a fixture for a new stage

To add a fixture for a stage that does not yet have one (e.g., code or fix):

1. Read the stage's result schema under `internal/scaffold/fullsend-repo/schemas/<stage>-result.schema.json`.
2. Copy the closest existing fixture and adapt it to satisfy the new schema's `required` fields and `additionalProperties: false` constraint.
3. Populate field values with plausible test data. Pay attention to:
   - **String patterns** — schemas may enforce regex patterns (e.g., `repo` must match `^[^/]+/[^/]+$`).
   - **Conditional requirements** — some schemas use `allOf`/`if`/`then` to require extra fields depending on the `action` value (e.g., `review-result` requires `head_sha` and `body` when `action` is `"comment"`).
   - **Downstream validation** — the harness post-script may apply stricter checks than the schema. For example, SHAs must be full-length hex, not truncated.
4. Place the fixture in `e2e/behaviour/fixtures/<stage>/<name>.json`.
5. Reference it in your scenario's dummy agent table with a `write_fixture` row targeting `output/agent-result.json`.

**Example:** The fork-bash-routing scenario dispatches a review agent, so it emits `review/comment.json` as the agent result:

```gherkin
Given a dummy agent that would:
  | description          | op            | args                                                       |
  | Prove bash routing   | write_fixture | output/bash-routing-ok.json, fixtures/dispatch/ok.json     |
  | Emit review JSON     | write_fixture | output/agent-result.json, fixtures/review/comment.json     |
```

The first row proves execution via an auxiliary file; the second row emits the schema-valid `agent-result.json` that the harness post-script requires.

## Running locally

```bash
# Local: gh auth login or export GH_TOKEN/GITHUB_TOKEN with access to halfsend org pool
make behaviour-test
```

### Parallel execution

The suite runs scenarios in parallel by default (`GODOG_CONCURRENCY=12`,
matching the repo pool size). Each scenario gets its own `World` clone and
leases a unique `test-repo-NN` from the pool, so no cross-scenario state
is shared. The `behaviour-test` Make target includes `-race` to catch
data races under concurrent execution.

To adjust concurrency:

```bash
# Run at default concurrency (12)
make behaviour-test

# Run with explicit concurrency
GODOG_CONCURRENCY=4 make behaviour-test

# Serial mode for debugging
GODOG_CONCURRENCY=1 make behaviour-test
```

Serial mode (`GODOG_CONCURRENCY=1`) is useful when debugging a single
scenario or when `-v` output from multiple scenarios would interleave.

In CI, the test runner mints cross-org `e2e` installation tokens via OIDC (same as admin e2e) for GitHub API operations. Triage workflows on the pool org's `test-repo` mint same-org `triage` tokens from vendored reusable workflows; those require per-repo mint enrollment (`PER_REPO_WIF_REPOS`) on the hosted mint project. Pool `test-repo` repos are enrolled once by a GCP admin — not during CI install. The install driver provisions repo-scoped inference WIF via `fullsend inference provision` before `github setup`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

### Repo allocation via unified Driver

The `Given the enrolled test repository` step allocates a repo via `Driver.AllocateRepo(ctx)`. The unified `install.Driver` (constructed by a `Factory` during suite setup) owns pool leasing and lazy create+install internally:

1. Leases a slot from the internal pool (blocks until one is free or ctx is cancelled).
2. Creates the repo if it does not exist (the forge's `auto_init` provides the initial commit).
3. Validates post-install files; if validation fails, runs `fullsend github setup` (and inference provision when configured).
4. Caches results by `org/repo` key so subsequent scenarios reuse the same State.

The After hook calls `Driver.DeallocateRepo` to return the slot. `Driver.Finalize` tears down suite-scoped resources (e.g. preview mint) and reclaims outstanding leases with an error.

Concurrent callers for the same repo are serialized via `singleflight.Group` — only one goroutine runs the create+install flow while others wait. This removes the requirement for numbered `test-repo-NN` repos to be pre-provisioned in the pool org.

**Suite duration:** Because each leased `test-repo-NN` pays create + inference provision + `github setup` on first use in a run, serial godog suites take longer than the old shared-`test-repo` model. CI budgets **45 minutes** for the behaviour job (`timeout-minutes` and `go test -timeout`) to match.

Runner env (defaults shown):

```
BEHAVIOUR_SCM=github
BEHAVIOUR_CI=githubactions
BEHAVIOUR_INSTALL_MODE=per-repo
ENVIRONMENT=dev               # mint/infra target: dev (default, local and PRs) or stage (push to main)
E2E_GCP_PROJECT_ID=...        # inference project; install runs inference provision per pool repo
E2E_GCP_WIF_PROVIDER=...      # CI job GCP auth (not written to pool test-repo secrets)
TEST_ACTOR_WRITE_PAT=...      # write-level human-like actor PAT (CI: same-named repo secret)
TEST_ACTOR_TRIAGE_PAT=...     # triage-level human-like actor PAT
TEST_ACTOR_OUTSIDER_PAT=...   # outsider human-like actor PAT (no org write on base)
```

`ENVIRONMENT` is `dev` or `stage`. Local runs default to `dev` when unset. CI sets it to match the GitHub Environment on the behaviour job (`dev` on pull requests and the merge queue, `stage` on push to `main`).

Triage scenarios apply the `ready-for-triage` label (not `/fs-triage` comments) because the per-repo shim ignores `issue_comment` events from bot users and CI uses minted e2e installation tokens.

### Test actor account permission scope

The three test actor accounts (`fstest-write`, `fstest-triage`, `fstest-outsider`) simulate human collaborators at different permission levels. Their access is deliberately contained so that exfiltrated PATs cannot modify production repositories.

| Property | fstest-write | fstest-triage | fstest-outsider |
|----------|-------------|---------------|-----------------|
| fullsend-ai org member | No | No | No |
| Permission on `fullsend-ai/fullsend` | Read | Read | Read |
| Permission on `fullsend-ai/agents` | Read | Read | Read |
| Write access | Pool-org `test-repo-NN` repos only | Pool-org `test-repo-NN` repos only | None (outsider) |

**Blast-radius containment:** All three accounts hold classic PATs. Because the accounts are not members of the `fullsend-ai` org and have only read permission on production repositories (`fullsend-ai/fullsend`, `fullsend-ai/agents`), a compromised PAT cannot push commits, merge PRs, or modify settings on any production repo. Write capability is scoped exclusively to disposable `test-repo-NN` infrastructure in the pool org, which is ephemeral and rebuilt each CI run.

**Re-verification guidance:** Re-verify account permissions whenever:

- A new test actor account is added
- An existing account is granted additional repository or org access
- The pool-org infrastructure changes (new orgs, new repo naming)

To verify, check org membership and repository permissions via the GitHub API:

```bash
# Check org membership (expect 404 for non-members)
gh api orgs/fullsend-ai/members/fstest-write --silent && echo "member" || echo "not a member"

# Check repo permission (expect "read" or "pull")
gh api repos/fullsend-ai/fullsend/collaborators/fstest-write/permission --jq '.permission'
```

Last verified: 2026-08-10 ([PR #6028 review](https://github.com/fullsend-ai/fullsend/pull/6028#pullrequestreview-3117093403)).

For the reusable test GitHub Apps (`fullsend-test-*`) used by temporary and test mints, see [Test GitHub Apps](e2e-testing.md#test-github-apps) in the e2e testing guide.

See [behaviour-drivers.md](behaviour-drivers.md) for driver configuration and [ADR 0066](../../ADRs/0066-behaviour-tests-with-gherkin-and-drivers.md) for the decision record.

## Fork PR scenarios

Fork dispatch scenarios test `pull_request_target` harness triggering from cross-fork pull requests.

### Logical fork name → leased base

Gherkin keeps a stable logical name (for example `"test-repo-fork"`). At runtime, `Given a fork` remaps that name to **`{World.RepoName}-fork`** when the scenario has leased a numbered base (for example leased `test-repo-07` → actual fork repo `test-repo-07-fork`). Feature files should keep using `"test-repo-fork"`; do not hard-code `test-repo-NN-fork` in Gherkin.

### Pool-org prerequisites

Fork scenarios require the pool org to have:

- **Permission to create forks** of the leased enrolled base (`test-repo-NN`) under the same org. The `Given a fork` step creates `{leased}-fork` idempotently when missing.
- **The same installation token** must have write access to both the base repo and the fork repo within the org, since the e2e bot commits to the fork and opens cross-fork PRs.

### Fork lifecycle

| Resource | Lifecycle | Cleanup |
|----------|-----------|---------|
| Fork repo | Per-scenario (`{RepoName}-fork`); created on demand | Deleted by `CleanupScenario` |
| Fork branches | Per-scenario | Deleted by `CleanupScenario` (before repo deletion) |
| Fork PRs | Per-scenario | Closed by `CleanupScenario` |

Fork repos are **ephemeral**: created when the `Given a fork` step runs and deleted by `CleanupScenario` after the scenario completes. Fork PRs are opened against the base repo (not the fork). `CleanupScenario` closes them via `CloseIssue` on the base repo, deletes the head branch on the fork repo, and then deletes the fork repo itself. Do **not** mint-enroll fork names — forks are PR sources only; mint stays on the enrolled base.

### Background step usage

Fork scenarios share a common `Background:` block that sets up the enrolled test repository and the fork:

```gherkin
Background:
  Given the enrolled test repository
  And a fork "test-repo-fork" of the enrolled test repository
```

The `Given a fork` step remaps the logical name as above and is idempotent for that actual fork repo. Each scenario then creates its own branch and PR within the fork.

### Fork PR behaviour contract

The `fork-dispatch.feature` file defines the canonical fork-PR behaviour contract for `harness-dispatch`. Each CEL port PR ([#2896](https://github.com/fullsend-ai/fullsend/issues/2896)–[#2901](https://github.com/fullsend-ai/fullsend/issues/2901)) should follow this contract when adding fork-PR rows to the agent's harness behaviour feature file.

| Scenario | Expected | How tested |
|----------|----------|------------|
| Fork PR matches CEL trigger; authorized actor | Agent runs via `harness-dispatch`; workflow completes | Positive dispatch + artifact assertion |
| Kill switch active (`kill_switch: true`) on fork event | Empty matrix, exit 0 | Separate scenario; `the kill switch is active` step + assert agent did not run |
| Disabled harness (`enabled: false`) on fork event | Empty matrix, exit 0 | Disabled harness in positive scenario; assert agent did not run |
| Fork PR `synchronize` + label dispatches harness | Harness dispatched exactly 1 time; workflow completes | Separate scenario with sync commit + label |
| CEL `is_fork` exclusion (`!event.state.change_proposal.is_fork`) | Empty matrix, exit 0 | Harness with `is_fork` guard in positive scenario; assert agent did not run |

**Kill switch vs disabled harness:** These are distinct mechanisms. The **kill switch** (`kill_switch: true` in `config.yaml`) is a global emergency stop that blocks *all* harness dispatch for the repo — tested in its own scenario because no positive harness can run alongside it. A **disabled harness** (`enabled: false` per agent entry) only prevents that single agent from running — tested as a piggyback negative assertion in the positive-path scenario.

**Consolidation pattern:** To conserve parallel execution slots, add negative-path harnesses (disabled agent, CEL exclusion) alongside the positive-path harness in a single scenario rather than creating separate scenarios. The positive harness wait acts as the settle window for negative assertions (piggyback pattern — see `negativeSettleDuration` in `dispatch.go`). The kill switch scenario cannot be consolidated because it blocks all harnesses.

**Unauthorized-actor denial** ([ADR 0054](../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)) for fork PRs is tracked separately in [#5613](https://github.com/fullsend-ai/fullsend/issues/5613) and is not part of this contract.

### Dispatch step reference

**`a disabled custom harness "<name>" with:`** — Registers the harness YAML under `.fullsend/harness/<name>.yaml` and adds an agent entry with `enabled: false` to the repo's `config.yaml`. Use this step for negative dispatch assertions where a single agent should be excluded while other agents in the same scenario continue to run. This is *not* the kill switch; for the global emergency stop that blocks all harnesses, use `the kill switch is active`.

**`the kill switch is active`** — Sets `kill_switch: true` in the repo's `config.yaml`, causing `Dispatch` to return an empty matrix for *all* agents. Use this step in a dedicated scenario where no harness should run. Because the kill switch blocks everything, it cannot share a scenario with a positive-path harness.

## Forge operational constraints

When modifying behaviour test repo provisioning, fork handling, or workflow dispatch, be aware of these constraints. They are not enforced by the compiler or linter — violations surface as cryptic API errors or silently dropped events in CI.

### `auto_init` creates an initial commit

The forge's `CreateRepo` uses `auto_init`, which creates an initial commit containing a README. Do **not** call `CreateFile("README.md")` (or any file that `auto_init` already provides) on a newly created repo — the GitHub API returns a 422 because the file already exists in the initial commit.

If a scenario needs to seed additional files, use a filename that does not collide with the `auto_init` commit (e.g., `seed.txt` instead of `README.md`), or check for existence first.

Reference: [`ensureRepoExists`](../../../pkg/behaviourtest/drivers/install/ensure.go) — see the `auto_init` comment and `CreateRepo` call.

### Fork name derivation depends on `World.RepoName`

The `Given a fork` step resolves the fork repo name by replacing the `test-repo` prefix with `World.RepoName`. For example, the logical Gherkin name `"test-repo-fork"` with a leased base `test-repo-07` resolves to `test-repo-07-fork`.

When modifying repo naming, leasing, or provisioning logic, verify that fork steps still resolve correctly. If `World.RepoName` changes (e.g., because leasing logic changes), fork resolution breaks — scenarios that use `Given a fork` will create or look for the wrong repo.

Reference: [`resolveForkName`](../../../pkg/behaviourtest/steps/fork.go) — maps logical fork names to actual repo names based on the leased base.

### Actions workflow readiness after repo creation

After creating a repo and committing workflow files via `fullsend github setup`, GitHub Actions needs time to index the workflow before it can receive dispatch events. Events dispatched before the workflow is indexed are **silently dropped** — no error is returned, but the workflow never runs.

The install driver's internal ensurer handles this by polling `GetWorkflow` until the workflow file is visible (up to 30 attempts with 5-second intervals). The function returns success as soon as the API returns a non-nil workflow object — it logs the workflow state but does not gate on it. When writing new provisioning code or modifying the install flow, always poll for workflow readiness before dispatching events that depend on the workflow.

Reference: [`awaitWorkflowReady`](../../../pkg/behaviourtest/drivers/install/ensure.go) — polls `GetWorkflow` until the workflow is visible to the API.

### CI timeout budgeting for lazy provisioning

Each lazily provisioned repo adds approximately 3–5 minutes of overhead (create + inference provision + `github setup` + Actions settle). The behaviour job's `timeout-minutes` in `e2e.yml` and the `go test -timeout` in the Makefile must account for this overhead across all leased repos in the suite.

Current budget: **45 minutes** for both the CI job timeout and `go test -timeout`. If adding scenarios that lease additional repos, verify that the total provisioning overhead plus test execution time fits within this budget. Adjust both values together — a `go test -timeout` higher than the CI `timeout-minutes` means the Go process is killed mid-test with no artifact collection.

Reference: [`.github/workflows/e2e.yml`](../../../.github/workflows/e2e.yml) behaviour job `timeout-minutes` and `Makefile` `behaviour-test` target.

## URL-sourced harness scenarios

URL dispatch scenarios test `FetchAgentHarness` URL resolution for agents whose harness YAML lives in a separate hosting repository rather than the local config directory.

### Harness-hosting repository

The `Given a harness-hosting repository "<name>"` step creates a public repository in the pool org to host harness YAML files. The repo is:

- **Ephemeral / per-scenario** — created per-scenario and deleted by `CleanupScenario` (same lifecycle as fork repos). When a leased repo is in use, the logical name is remapped via `resolveHostRepoName` (e.g. `"url-harness-host"` + leased `"test-repo-07"` → `"test-repo-07-url-harness-host"`) so parallel scenarios each get their own isolated hosting repo.
- **Public** — required for unauthenticated `raw.githubusercontent.com` access. The step calls `EnsureRepoPublic` to detect and fix org policies that force repos private.

### URL-sourced custom harness

The `Given a URL-sourced custom harness "<name>" with:` step:

1. Commits the harness YAML to the hosting repo at `harness/<name>.yaml`
2. Commits any relative resources (agent, policy files) referenced in the YAML (ADR-0045)
3. Verifies accessibility via the Contents API and unauthenticated raw URL
4. Registers the agent in `config.yaml` with the raw URL (including `#sha256=` integrity hash)
5. Adds the hosting repo URL prefix to `allowed_remote_resources`

Variants:
- `with bad integrity hash:` — injects a wrong SHA256 to test integrity failure
- `not in allowlist with:` — omits the URL prefix from the allowlist to test validation

### Background step usage

URL dispatch scenarios share a common `Background:` block:

```gherkin
Background:
  Given the enrolled test repository
  And a harness-hosting repository "url-harness-host"
```

### FetchPolicy and binary freshness

URL-dispatch scenarios require a vendored CLI binary that includes `FetchPolicy`-aware harness dispatch. Production dispatch uses `fetch.DefaultPolicy` (allows `github.com` and `raw.githubusercontent.com`) when `Options.FetchPolicy` is nil — this is what enables URL-sourced agents to resolve `raw.githubusercontent.com` URLs.

The install driver's internal ensurer always re-vendors the CLI binary (`github setup --vendor`) even when a prior install's post-install validation passes. This guarantees leased pool repos run the binary built from the current checkout rather than a stale binary from a previous CI run. Without re-vendoring, pool repos that passed validation would keep a pre-fix binary and silently fail to dispatch URL-sourced agents.

The settle step (polling for GitHub Actions workflow readiness) is skipped on re-vendors since the workflow file already existed — only fresh installs incur the settle wait.

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

**`suite.InitScenario` signature change:** The function signature changed from `InitScenario(sc, template, pool)` to `InitScenario(sc, template)`. The `*world.RepoPool` type has been removed. Repo leasing is handled internally by the unified `install.Driver` on `template.Driver`. Callers construct a `Driver` via a `Factory` and set it on the template World:

```go
driver, err := install.NewRepoPoolCFMintPreviews(org, client, token, binary, gcpProjectID, t.Logf)
if err != nil {
    t.Fatalf("creating driver: %v", err)
}
t.Cleanup(func() {
    if err := driver.Finalize(context.Background()); err != nil {
        t.Logf("driver finalize: %v", err)
    }
})

template := &world.World{Driver: driver, /* ... other fields ... */}

suiteRunner := godog.TestSuite{
    ScenarioInitializer: func(sc *godog.ScenarioContext) {
        suite.InitScenario(sc, template)
    },
    // ...
}
```

**Concrete drivers renamed:** `cfmint` → `RepoPoolCFMintPreviews`, `legacy` / `externalmint` → `RepoPoolExternalMint`. Drivers are named for the environments they manage. Concrete implementations live in the `install` package. `install.Factory` takes `(org string, client forge.Client, token, binary, gcpProjectID string, logf func(string, ...any))`; driver-specific config (PEMs, pool size, mint URL) is read from env or computed internally. `install.State`, `install.MintURLProvider`, `install.RepoEnsurer`, and `install.CFMintConfig` are removed from the exported surface. External code should only reference `install.Factory` and `install.Driver`.

**`world.World.Install` removed:** The `Install install.State` field on `World` is removed. Steps use `w.Org` + `w.RepoName` (the allocated repo name) and per-repo constants from the `install` package (`PerRepoTriageWorkflow`, `PerRepoAgentWorkflow`, `PerRepoAgentArtifact`) instead of config indirection through `State`.

**`world.World.Ensurer` replaced with `world.World.Driver`:** The `Ensurer` field on `World` is replaced by `Driver install.Driver` (the unified driver). External code that set `w.Ensurer` must set `w.Driver` instead — the driver handles both pool leasing and ensure internally.

**`steps.Register` signature change:** The function signature changed from `Register(ctx, w)` (where `ctx` was a `*godog.ScenarioContext` and `w` was a `*world.World`) to `Register(sc)` starting in the same release. Step definitions no longer receive `*world.World` as a parameter. Instead, they accept `context.Context` and extract the per-scenario World via `world.FromContext(ctx)`.

**`scm.Driver.DeleteRepo` addition:** The `scm.Driver` interface now includes a `DeleteRepo(ctx context.Context, owner, repo string) error` method. `CleanupScenario` calls it to delete ephemeral fork repos after each scenario. External `scm.Driver` implementations must add this method — return `forge.ErrNotFound` when the repository does not exist.

**`scm.Driver.ListOpenChangeProposals` / `scm.Driver.ListComments` additions:** `ListOpenChangeProposals(ctx, owner, repo) ([]forge.ChangeProposal, error)` returns the repository's **open** pull requests including each head branch; `ListComments(ctx, owner, repo, number) ([]forge.IssueComment, error)` returns the comments on an issue or pull request. The branch assertion steps and the scenario-cleanup namespace sweep call them. External `scm.Driver` implementations must add both methods.

**`ci.Driver.WaitForFailedHarnessAgent` addition:** `WaitForFailedHarnessAgent(ctx, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error)` waits for the named agent's harness run to complete with a terminal failure conclusion (artifact-first detection, job-name fallback) and errors out early when the run succeeds instead. External `ci.Driver` implementations must add this method.

Bump the pinned version when behaviour step vocabulary or `pkg/e2etest` / `pkg/behaviourtest` APIs change.
