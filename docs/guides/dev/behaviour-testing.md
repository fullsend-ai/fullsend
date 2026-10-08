# Behaviour testing

End-to-end Gherkin tests under `e2e/behaviour/` validate **deterministic platform code** with inference removed. They are **orthogonal** to LLM and instruction testing in [testing-agents.md](../../problems/testing-agents.md).

| | Behaviour tests | LLM evals | Unit tests |
|---|-----------------|-----------|------------|
| **Target** | Platform workflows, sandbox, SCM | Prompts, models | Go functions |
| **Inference** | Dummy runtime | Real LLM | N/A |
| **Infrastructure** | Live GitHub + GHA | Varies | None |

## When to add a behaviour test

Add one when a **user-visible workflow** must be verified end-to-end (dispatch → workflow → post-script → SCM state) and the assertion is **binary**. Prefer unit tests for pure Go logic.

## Layout

Shared framework (importable by external repos):

```
pkg/behaviourtest/   # RunSuite public entry (build tag: behaviour);
                     # RunPlaybackSuite public entry (build tag: playback)
  world/             # Scenario state
  steps/             # Step definitions + CleanupScenario
  artifacts/         # Artifact lookup helpers
  drivers/           # SCM, CI, env, install interfaces + v1 impls
  suite/             # InitScenario (tags, hooks, step registration)
```

In-repo live-test infrastructure (not a public API):

```
internal/e2etest/    # Org pool, CLI runner, cleanup
```

In-repo runner and scenarios:

```
e2e/behaviour/
  features/          # Portable Gherkin scenarios
  fixtures/          # Static content for write_fixture ops
  suite_test.go      # Thin RunSuite caller (build tag: behaviour)
  playback_suite_test.go  # Thin RunPlaybackSuite caller (build tag: playback)
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

- **Core (every run):** `Then the run selected the "dummy" runtime` reads the `runtime` field the runner writes into `metrics.json`, proving the repo's `.fullsend/config.yaml` `runtime:` reached backend selection (or `config.base.yaml` when `BEHAVIOUR_CONFIG_PRESET` supplied a preset). Use it in one representative scenario per stage; the artifact is already downloaded for the other assertions.
- **Runtime-specific (gated):** `Given the repository runtime is "<name>"` commits `runtime: <name>` to the leased repo's config for this scenario only (CleanupScenario restores `dummy` — slots are reused, so never set it any other way; the step refuses if the slot is not on `dummy` to begin with). The custom-harness step commits only a placeholder for a relative `agent:` path, which a real runtime cannot act on, so follow it with the agent step for the runtime under test (`And a pi agent "<name>" defined as:`, `And a codex agent "<name>" defined as:` — both commit the same file) and a docstring holding the full agent file (frontmatter + body) — `{{fixture:fixtures/<stage>/<file>.json}}` inlines a result fixture so the model has a concrete, deterministic file to write (the custom harness carries no post-script, so nothing validates it; the assertions are on the transcript and metrics). Then the scenario dispatches the harness and asserts on artifacts: `the run selected the "pi" runtime`, `the pi session transcript records at least one tool call` (the agent used a tool through pi; with security enabled the run refuses to start without the intact hook adapter, so the call was mediated by it — the step does not inspect hook output), `the run metrics report tokens`. Such scenarios cost a real model run on the pool repo's repo-scoped Vertex WIF and must be tagged `@requires:capability:runtime-<name>` so they only run where the runner declares the capability; `make behaviour-test` declares `runtime-pi` by default (a `Makefile` variable, so a PR adding a gated scenario exercises it on its own `pull_request_target` run — the workflow file itself comes from `main`); `BEHAVIOUR_CAPABILITIES= make behaviour-test` skips them. See `features/runtime/pi.feature`. `features/runtime/pi-openai.feature` is the same shape on `openai/gpt-5.6-luna` with the `openai` provider instead of Vertex host files; it is gated on `runtime-pi-openai`, which is **not** declared by default because it needs an OpenAI organization mapped to the pool repositories plus their `FULLSEND_OPENAI_*` variables ([OpenAI Workload Identity](../infrastructure/openai-workload-identity.md)). `features/runtime/codex-openai.feature` is that same shape on the codex runtime — `And a codex agent "<name>" defined as:` for the agent, and `the codex output stream records at least one tool call`, which reads the tee'd `codex exec --json` stream (`output.jsonl`) rather than a session transcript. It is gated on `runtime-codex-openai` for the same reason, and codex has no Vertex path, so — unlike pi, whose `runtime-pi` scenario runs on every job — codex has **no default behaviour coverage at all** until that organization exists; its evidence until then is unit tests, recorded fixtures and local smoke runs.
- **Per-agent (every run):** `Given the repository agents are configured with:` with a YAML docstring (`triage:\n  runtime: dummy`) sets runtime/model/effort on the leased repo's `agents:` entries (a name-only entry for a built-in, the sourced entry for a custom agent; only the settings given change) — validated the way `fullsend run` validates them — and CleanupScenario restores the pre-scenario `agents:` list. Pair it with `the repository runtime is "<real runtime>"` and pin every agent the scenario can dispatch (triage hands off to `code` via `ready-to-code`) back to `dummy`, then assert `the run selected the "dummy" runtime from "agents.triage"`, which also checks `runtime_source` in `metrics.json` ends with that entry — proof the per-agent entry decided, at dummy cost. The gated second scenario in the same file leaves the repo on `dummy` and puts one custom agent on pi with `model: haiku` from its entry (the harness says `opus`); `the run requested model "haiku" from "agents.<name>" and the provider reported a "haiku" model` checks `requested_model`, `override_source`, the reported `model` and `num_turns` in `metrics.json`. See `features/runtime/agent-settings.feature`.

Do not add runtime coverage behind new `fullsend admin` flags. Use the repository config steps above.

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
review from a review bot, which the suite cannot produce — but the
step is unit-tested and ready for a suite-reachable fail-closed path.

### Compatibility tags

Use tags only for **exceptions** when a backend cannot run a scenario yet, such as `@skip:gitlab`. Untagged scenarios run everywhere applicable.

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

### Playback suite

The dummy-playback scenarios (`@playback`, e.g. `features/runtime/playback.feature`) run under their own target:

```bash
make playback-test
```

The target runs `go test -tags playback -race -v -count=1 -timeout 45m ./e2e/behaviour/`. It uses only the `playback` build tag, so only `TestPlaybackSuite` (`behaviourtest.RunPlaybackSuite`) is compiled and `TestBehaviourSuite` is left out. Do not add `behaviour` to the tag list; that would run both suites. `RunPlaybackSuite` fixes the godog tag filter to `@playback` (`GODOG_TAGS` is ignored) and sets `PLAYBACK_RUNTIME=dummy-playback` for the install driver. Everything else is configured from the same environment as `make behaviour-test`: `ENVIRONMENT`, `BEHAVIOUR_SCM` / `BEHAVIOUR_CI` / `BEHAVIOUR_INSTALL_MODE`, `E2E_GCP_*`, `E2E_LOCK_TIMEOUT`, `GODOG_CONCURRENCY`, `BEHAVIOUR_ARTIFACT_DIR`, and the role PEM / Cloudflare credentials the install factories need. `make behaviour-test` skips `@playback` scenarios, so the two targets never run the same scenario.

#### Running the playback suite in CI

The `playback` job mirrors the `behaviour` job in [`.github/workflows/e2e.yml`](../../../.github/workflows/e2e.yml), with these specifics:

- **Target and environment.** Run `make playback-test` with `shell: bash`, teeing output (for example to `playback-test.log`) so `pipefail` propagates failures. Pass the same `env:` block as the behaviour job: `BEHAVIOUR_SCM=github`, `BEHAVIOUR_CI=githubactions`, `BEHAVIOUR_INSTALL_MODE=per-repo`, `E2E_GCP_*`, `TEST_*_PEM`, and the `TEST_CLOUDFLARE_*` credentials. Point `BEHAVIOUR_ARTIFACT_DIR` at a playback-specific directory (for example `${{ runner.temp }}/playback-artifacts`). You do not need to set `GODOG_TAGS` or `PLAYBACK_RUNTIME`, because `RunPlaybackSuite` sets them itself.
- **Triggers, gate, and environment binding.** Use `needs: gate` for authorized `pull_request_target`, `merge_group`, and push to `main`; manual dispatch runs playback when `run_playback=true`. Bind to the same GitHub Environment (`stage` on push, `dev` otherwise) and set `ENVIRONMENT` to match. The job checks out and runs PR-head code with secrets, so the [CI Workflows security rules](../../contributing/ci-workflows.md#review-checklist-for-secrets-in-behaviourplayback-jobs) apply.
- **Org reservation.** The playback and behaviour jobs share a job concurrency group within each stage workflow run, so they execute one at a time. Dev jobs use distinct groups and run concurrently, reserving separate orgs. `RunPlaybackSuite` reserves a pool org with the same `e2etest.AcquireOrg` lock that `RunSuite` uses (`orgPoolForEnvironment`), and the lock is released in `t.Cleanup`. Two limits make relying on that lock alone unsafe when both suites target the single stage org:
  - `AcquireOrg` waits for the lock for the `E2E_LOCK_TIMEOUT` default of 10 minutes, far less than the 45-minute suite budget, so a job that starts while the other suite is running can fail while waiting.
  - A lock is treated as stale and reclaimed once it is older than 30 minutes (`staleLockTimeout` in `internal/e2etest`). That check uses only the lock's creation time and does not check whether the holder is still running, so a later contender can reclaim an org that a long-running suite is still using.

  On `dev`, the pool has several orgs, so contention is lower, but the same limits apply when the pool is exhausted. The org lock remains responsible for reservation; job concurrency prevents contention between these two suites in the same stage run.
- **Change detection.** Reuse the behaviour job's file filter (its `grep -qE` path list and the `push.paths` entries under the same `SYNC-WITH` comment). It already covers `e2e/behaviour/` (playback features, fixtures and results), `pkg/behaviourtest/`, `internal/runtime/` (the `dummy-playback` runtime), and `Makefile`. Keep the two filters in sync, and keep the behaviour job's fallback of running the tests when the file list cannot be fetched or may be truncated.
- **Cancellation.** The job inherits the workflow-level `concurrency` group and `cancel-in-progress` expression. A cancelled or killed run cannot run `t.Cleanup`, so its org lock is reclaimed by the stale-lock timeout in `internal/e2etest`, exactly as for the behaviour job. Gate post-test steps on `always()` rather than `success()` so that redaction still runs after a failure.
- **Artifacts.** Before uploading, redact the playback artifact directory with the base-branch `scripts/redact-behaviour-artifacts.sh`, run through `env -i` with the full secret list, as described in [Behaviour debug artifact redaction](../../contributing/ci-workflows.md#behaviour-debug-artifact-redaction). Upload only when `steps.redact.outcome == 'success'` and the suite step failed, with `if-no-files-found: ignore`. Use an artifact name distinct from `behaviour-artifacts-*`, for example `playback-artifacts-<pr-or-run-id>`.
- **Timeout.** Set `timeout-minutes: 45` to match the target's `go test -timeout` (see [CI timeout budgeting](#ci-timeout-budgeting-for-lazy-provisioning)).

To run playback from a trusted PR branch before merging, dispatch the existing E2E workflow with that branch as the ref:

```bash
gh workflow run e2e.yml --repo fullsend-ai/fullsend \
  --ref agent/7942-playback-test-target -f run_playback=true
```

This runs the branch's workflow and test target in the dev environment. The regular `behaviour` job is skipped for this playback dispatch. Artifact redaction uses the default-branch script.

In CI, the test runner mints cross-org `e2e` installation tokens via OIDC for GitHub API operations. Triage workflows on the pool org's `test-repo` mint same-org `triage` tokens from vendored reusable workflows; those require per-repo mint enrollment (`PER_REPO_WIF_REPOS`) on the hosted mint project. Pool `test-repo` repos are enrolled once by a GCP admin — not during CI install. Before `github setup`, the install driver resolves the repo-scoped inference WIF provider: it runs `fullsend inference status` and runs `fullsend inference provision` only when the provider is not healthy. Provisions are serialised across the process. The resolved provider is cached per repo name for the rest of the run, including after the repo is deleted and recreated, because the provider ID, its attribute condition and the Vertex AI grant are all keyed by `owner/repo`, not by repo ID (`Provisioner.ProvisionWIF` in `internal/dispatch/gcf/provisioner.go` creates the provider and the grant, and is the source of truth for these bindings). See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

### Repo allocation via unified Driver

The `Given the enrolled test repository` step allocates a repo via `Driver.AllocateRepo(ctx)`. The unified `install.Driver` (constructed by a `Factory` during suite setup) owns pool leasing and lazy create+install internally:

1. Leases a slot from the internal pool (blocks until one is free or ctx is cancelled).
2. If the repo already exists, deletes it (and any leftover `{name}-fork`) so the scenario cannot inherit labels, branches, PRs, workflow runs, or config from a previous lessee.
3. Creates the repo (the forge's `auto_init` provides the initial commit) and runs `fullsend github setup` (after resolving the inference WIF provider when configured).
4. Caches the successful ensure for the duration of this lease so duplicate `EnsureRepo` calls skip redundant work.

The After hook runs `CleanupScenario` (issues, PRs, ephemeral forks, hosting repos) and then `Driver.DeallocateRepo`, which deletes the leased base — after in-scenario debug collection has written workflow logs and agent artifacts under `BEHAVIOUR_ARTIFACT_DIR` — and returns the name to the pool. For a failed scenario the After hook first runs `steps.CollectFailureLogs`. It saves the logs of the repository's recent workflow runs that no step already saved, which covers a wait that timed out or a step that failed before it resolved a run. It always writes `debug-scenario-<name>-<suffix>/failure-summary.txt` with the scenario error and each run's outcome. A run whose logs cannot be fetched gets a `workflow-logs-unavailable.txt` stating why. The summary states when the run listing may be truncated or the per-scenario fetch cap was reached. A scenario that passes but whose repo deallocation fails also gets a failure summary. Everything is redacted and written `0600`, like the in-scenario logs; the redaction also masks the runner's `World.Token` and the values of credential-named environment variables (for example `*_TOKEN`, `*_PAT`, `*_KEY`), including in directory names. The next lessee of that name recreates the repo from scratch. Mint enrollment of the numbered *names* can remain pre-provisioned; the GitHub repos themselves are ephemeral around a lease. `Driver.Finalize` tears down suite-scoped resources (e.g. preview mint) and reclaims outstanding leases with an error.

Concurrent callers for the same repo are serialized via `singleflight.Group` — only one goroutine runs the create+install flow while others wait. This removes the requirement for numbered `test-repo-NN` repos to be pre-provisioned in the pool org.

**Credential context separation:** The suite's e2e installation token and dispatch's per-repo `GITHUB_TOKEN` are distinct credential contexts with independent permission propagation graphs. After a pool repo is deleted and recreated, the suite can confirm the repo exists (via `GetRepo`), but it **cannot** observe or predict when dispatch-side collaborator permissions will be ready. Do not add `GetCollaboratorPermission` polling to the suite-side readiness checks — the suite's token resolves permissions through a different GitHub subsystem than dispatch's token. See the [package doc comment](../../../pkg/behaviourtest/drivers/install/doc.go) for details and the empirical evidence from [#6701](https://github.com/fullsend-ai/fullsend/issues/6701).

**Suite duration:** Because each lease of `test-repo-NN` pays create + `github setup` (not only the first use in a run), serial godog suites take longer than a shared-repo model. CI budgets **45 minutes** for the behaviour job (`timeout-minutes` and `go test -timeout`) to match.

Runner env (defaults shown):

```
BEHAVIOUR_SCM=github              # also: gitlab; future: forgejo
BEHAVIOUR_CI=githubactions        # also: gitlabci; future: tekton
BEHAVIOUR_INSTALL_MODE=per-repo
BEHAVIOUR_APP_SET=fullsend-test # app identities for both mint deployment and github setup; must match the supplied role PEMs
BEHAVIOUR_ARTIFACT_DIR=        # CI upload-artifact root for debug logs and run artifacts; temp dir when unset
BEHAVIOUR_CONFIG_PRESET=       # optional local path or HTTPS URL forwarded as github setup --config
PLAYBACK_RUNTIME=              # unset: normal "dummy" runtime; "dummy-playback": install.PlaybackDriver's installation runtime
ENVIRONMENT=dev               # mint/infra target: dev (default, local and PRs) or stage (push to main)
E2E_GCP_PROJECT_ID=...        # inference project; install resolves (and if needed provisions) inference WIF once per pool repo name
E2E_GCP_WIF_PROVIDER=...      # CI job GCP auth (not written to pool test-repo secrets)
TEST_ACTOR_WRITE_PAT=...      # write-level human-like actor PAT (CI: same-named repo secret)
TEST_ACTOR_TRIAGE_PAT=...     # triage-level human-like actor PAT
TEST_ACTOR_OUTSIDER_PAT=...   # outsider human-like actor PAT (no org write on base)
```

`ENVIRONMENT` is `dev` or `stage`. Local runs default to `dev` when unset. CI sets it to match the GitHub Environment on the behaviour job (`dev` on pull requests and the merge queue, `stage` on push to `main`).

When `BEHAVIOUR_CONFIG_PRESET` is set to a local path or HTTPS URL, install drivers forward it as `fullsend github setup --config <value>` and omit `--runtime dummy` so the preset's `runtime: dummy` is inherited rather than pinned in the overlay. Unset (the default) leaves install behaviour unchanged.

`PLAYBACK_RUNTIME` only affects provisioning: when set (e.g. to `dummy-playback`), the DEV and STAGE factories install repos with that runtime (passed as `GitHubSetupOpts.Runtime`) and attach `playbackInstallHooks`, which creates the per-repo playback tracking issue/comment after install. It does not itself select `install.PlaybackDriver` as the suite's `Driver` — that wrapper (which `World.IsPlaybackMode` and the shared playback step definitions rely on) is constructed separately by the caller wrapping the factory's returned `Driver` in `install.NewPlaybackDriver`.

When `ENVIRONMENT=stage`, the suite selects the `RepoPoolCFMintStage` driver which deploys a durable CF Worker mint at `stage-mint.fullsend.sh` and uses the `halfsend` org with a non-vendored per-repo install (referencing main HEAD via `--fullsend-ref=main`). The `halfsend` org uses the same repo pool pattern as the DEV pool orgs.

Triage scenarios apply the `ready-for-triage` label (not `/fs-triage` comments) because the per-repo shim ignores `issue_comment` events from bot users and CI uses minted e2e installation tokens.

### Test actor account permission scope

The three test actor accounts (`fstest-write`, `fstest-triage`, `fstest-outsider`) simulate human collaborators at different permission levels. Their access is deliberately contained so that exfiltrated PATs cannot modify production repositories.

| Property | fstest-write | fstest-triage | fstest-outsider |
|----------|-------------|---------------|-----------------|
| fullsend-ai org member | No | No | No |
| Permission on `fullsend-ai/fullsend` | Read | Read | Read |
| Permission on `fullsend-ai/agents` | Read | Read | Read |
| Write access | Pool-org repos via all-repository write (DEV `halfsend-NN`, STAGE `halfsend`) | Pool-org repos via all-repository triage (DEV `halfsend-NN`, STAGE `halfsend`) | None (outsider) |

**Blast-radius containment:** All three accounts hold classic PATs. Because the accounts are not members of the `fullsend-ai` org and have only read permission on production repositories (`fullsend-ai/fullsend`, `fullsend-ai/agents`), a compromised PAT cannot push commits, merge PRs, or modify settings on any production repo. Write and triage capability comes from all-repository organization roles on the DEV pool orgs (`halfsend-NN`) and the `halfsend` STAGE org, so it survives the ephemeral `test-repo-NN` delete/recreate lifecycle. No write access extends beyond these test-only organisations. The outsider account is not an org member and has no all-repository role.

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

### Repo deletion can lag behind `GetRepo`

After `DeleteRepo`, GitHub may keep serving a cached repository object for several seconds. A successful `GetRepo` immediately after delete does **not** mean the name is still taken — and it also does not mean the repo will still exist by the time `fullsend github setup` runs. Treating that stale success as "already ready" skips recreation and then 404s during setup.

GitHub's reads are not monotonic here: a confirmed 404 can be followed by a stale 200, and a 404 does not guarantee `CreateRepo` will accept the name yet. So the install ensurer waits for a 404 (`awaitDeletion`) and then always calls `CreateRepo`, never trusting a single `GetRepo`. If creation fails because the name is still taken, it deletes the repository blocking creation, and any leftover `-fork`, with `deleteBlockingRepo`. It waits for that deletion to propagate, backs off exponentially, and retries `CreateRepo`. It fails the allocation if the name never becomes free within the attempt budget.

Reference: [`ensureRepoExists`](../../../pkg/behaviourtest/drivers/install/ensure.go), [`deleteBlockingRepo`](../../../pkg/behaviourtest/drivers/install/ensure.go) and [`awaitDeletion`](../../../pkg/behaviourtest/drivers/install/ensure.go).

### Fork name derivation depends on `World.RepoName`

The `Given a fork` step resolves the fork repo name by replacing the `test-repo` prefix with `World.RepoName`. For example, the logical Gherkin name `"test-repo-fork"` with a leased base `test-repo-07` resolves to `test-repo-07-fork`.

When modifying repo naming, leasing, or provisioning logic, verify that fork steps still resolve correctly. If `World.RepoName` changes (e.g., because leasing logic changes), fork resolution breaks — scenarios that use `Given a fork` will create or look for the wrong repo.

Reference: [`resolveForkName`](../../../pkg/behaviourtest/steps/fork.go) — maps logical fork names to actual repo names based on the leased base.

### Fork name uniqueness lags deletion

GitHub's repository-name uniqueness constraint can lag `DeleteRepo`. `GetRepo` may already 404 while creating a fork with the same name still returns `403 Name already exists on this account`. `CleanupScenario` deletes the fork without waiting for uniqueness — a 404 is not a sufficient signal. The `Given a fork` step retries `CreateFork` on that collision until GitHub releases the name.

Reference: [`createFork`](../../../pkg/behaviourtest/steps/fork.go) — retries name-collision errors from `CreateFork`.

### Actions workflow readiness after repo creation

After creating a repo and committing workflow files via `fullsend github setup`, GitHub Actions needs time to index the workflow before it can receive dispatch events. Events dispatched before the workflow is indexed are **silently dropped** — no error is returned, but the workflow never runs.

The install driver's internal ensurer handles this by polling `GetWorkflow` until the workflow file is visible (up to 30 attempts with 5-second intervals). The function returns success as soon as the API returns a non-nil workflow object — it logs the workflow state but does not gate on it. When writing new provisioning code or modifying the install flow, always poll for workflow readiness before dispatching events that depend on the workflow.

Reference: [`awaitWorkflowReady`](../../../pkg/behaviourtest/drivers/install/ensure.go) — polls `GetWorkflow` until the workflow is visible to the API.

### CI timeout budgeting for lazy provisioning

Each lease of a pool repo adds approximately 3–5 minutes of overhead (delete leftover state + create + `github setup` + Actions settle; the first lease of each name also resolves inference WIF), including when a later scenario reuses the same `test-repo-NN` name. The behaviour job's `timeout-minutes` in `e2e.yml` and the `go test -timeout` in the Makefile must account for this overhead across all leases in the suite.

Current budget: **45 minutes** for both the CI job timeout and `go test -timeout`. If adding scenarios that lease additional repos (or increase reuse of the 12-slot pool), verify that the total provisioning overhead plus test execution time fits within this budget. Adjust both values together — a `go test -timeout` higher than the CI `timeout-minutes` means the Go process is killed mid-test with no artifact collection. The same rule applies to the `playback-test` target (also 45 minutes) and to any CI job that runs it.

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

`doEnsure` always resets (delete + recreate), installs, and settles: every lease starts from a freshly created repo, so there is no re-vendor path that skips the settle wait — the settle step runs on every ensure.

## Version pinning for `fullsend-ai/agents`

External behaviour runners import the shared libraries from this module:

```go
require github.com/fullsend-ai/fullsend v0.x.y // released tag, not @main
```

Do not import `internal/mintcore` (or `internal/mintcore/mintconsts`) from packages reachable from `pkg/behaviourtest`. The nested mintcore module is resolved only by a local `replace` that downstream modules do not inherit; a leak makes `go build github.com/fullsend-ai/fullsend/pkg/behaviourtest` fail with `unknown revision internal/mintcore/v0.0.0`. Duplicate constants locally and keep the graph clean — see [Go Code](../../contributing/go-code.md).

The supported entry point is `behaviourtest.RunSuite`. Driver selection, org acquisition, CLI build, concurrency, tags, and step registration are handled internally from the same environment variables as the in-repo suite (`BEHAVIOUR_SCM`, `BEHAVIOUR_CI`, `BEHAVIOUR_INSTALL_MODE`, `ENVIRONMENT`, `BEHAVIOUR_CAPABILITIES`, `BEHAVIOUR_CONFIG_PRESET`, `GODOG_TAGS`, `GODOG_CONCURRENCY`):

```go
//go:build behaviour

package behaviour_test

import (
    "testing"

    "github.com/fullsend-ai/fullsend/pkg/behaviourtest"
)

func TestBehaviourSuite(t *testing.T) {
    behaviourtest.RunSuite(t, behaviourtest.SuiteOptions{
        FeaturePaths: []string{"features"},
        FixturesRoot: "behaviour", // module-relative; "e2e/behaviour" in this repo
    })
}
```

| Field | Meaning |
|-------|---------|
| `FeaturePaths` | Godog feature file or directory paths, relative to the test working directory |
| `FixturesRoot` | Module-relative directory that contains `fixtures/` |

`RunSuite` builds the CLI from module `github.com/fullsend-ai/fullsend` (equivalent to `e2etest.BuildModuleBinary`), so the caller's module root is not used. Run with `-tags behaviour` and the same env vars as CI (see above).

The dummy-playback suite has its own entry point, `behaviourtest.RunPlaybackSuite`, which takes the same `SuiteOptions` and is called from a test file built with `-tags playback` (see `e2e/behaviour/playback_suite_test.go`). It installs pool repos with the `dummy-playback` runtime, runs only `@playback`-tagged scenarios (the filter is fixed; `GODOG_TAGS` is not consulted), and is otherwise configured from the same environment variables as `RunSuite`. `@playback` scenarios are skipped automatically by the standard `RunSuite` suite, so the two runners do not overlap. In this repo, `make playback-test` runs it (see [Playback suite](#playback-suite)). External callers should build it with `-tags playback`:

```go
//go:build playback

package behaviour_test

import (
    "testing"

    "github.com/fullsend-ai/fullsend/pkg/behaviourtest"
)

func TestPlaybackSuite(t *testing.T) {
    behaviourtest.RunPlaybackSuite(t, behaviourtest.SuiteOptions{
        FeaturePaths: []string{"features"},
        FixturesRoot: "e2e/behaviour", // module-relative
    })
}
```

Lower-level packages (`world`, `steps`, `drivers`, `suite.InitScenario`) remain available for custom bootstraps. Org pool and CLI helpers live in `internal/e2etest` and are not importable outside this module. Prefer `RunSuite` unless you need to inject drivers the env-based selector does not cover.

### API changes

**`behaviourtest.RunPlaybackSuite`:** New entry point for the dummy-playback suite (build tag: `playback`). Callers pass the same `SuiteOptions{FeaturePaths, FixturesRoot}`; the `@playback` tag filter is fixed.

**`behaviourtest.RunSuite`:** New high-level entry point. Callers pass `SuiteOptions{FeaturePaths, FixturesRoot}` only. Replaces the ~80-line bootstrap previously duplicated in `e2e/behaviour/suite_test.go`.

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

**`scm.Driver.ListPullRequestReviews` addition (breaking change):** The `scm.Driver` interface now includes `ListPullRequestReviews(ctx, owner, repo, number) ([]forge.PullRequestReview, error)`, returning the formal reviews submitted on a change proposal. The GitHub and GitLab reference implementations pass through to the existing `forge.Client` method of the same name. This widens the required method set, so external `scm.Driver` implementations must add this method when upgrading past this release or they will no longer satisfy the interface.

**`ci.Driver.WaitForHarnessAgentRound` addition (breaking change):** The `ci.Driver` interface now includes `WaitForHarnessAgentRound(ctx, owner, repo, agent string, after time.Time, consumed map[int]bool) (*forge.WorkflowRun, error)`, for scenarios where the same agent's harness is dispatched more than once (e.g. dummy-playback's review round, retried after fix). Unlike `WaitForHarnessAgent`'s latest-eligible-run selection, it selects the earliest eligible run whose ID is not in `consumed`. This widens the required method set, so external `ci.Driver` implementations must add this method when upgrading past this release or they will no longer satisfy the interface.

**`scm.Driver.GetFileContentAtRef` addition (breaking change):** The `scm.Driver` interface now includes `GetFileContentAtRef(ctx, owner, repo, path, ref string) ([]byte, error)`, retrieving a file's content at a specific ref (commit SHA, branch, or tag) rather than `GetFileContent`'s implicit default-branch/HEAD read. The dummy-playback suite's "the published repository matches fixture" step uses it to verify, file by file, that a stage actually published the expected content at a pinned commit. The GitHub and GitLab reference implementations pass through to the existing `forge.Client` method of the same name. This widens the required method set, so external `scm.Driver` implementations must add this method when upgrading past this release or they will no longer satisfy the interface.

**`scm.Driver.ListPullRequestCommits` addition (breaking change):** The `scm.Driver` interface now includes `ListPullRequestCommits(ctx, owner, repo, number) ([]string, error)`, returning the commit SHAs on a pull request, oldest first. The dummy-playback suite uses the first entry as the code stage's published commit: review and fix only append commits to the branch, so unlike the head branch's live tip, that commit cannot be displaced by a later stage that races ahead. The GitHub and GitLab reference implementations pass through to the new `forge.Client.ListPullRequestCommits` method (GitHub caps results at 250 commits). This widens the required method set, so external `scm.Driver` implementations must add this method when upgrading past this release or they will no longer satisfy the interface.

**`ci.RunLister` optional interface:** `ListRecentRuns(ctx, owner, repo string, limit int) ([]forge.WorkflowRun, error)` lists a repository's most recent workflow runs across all workflows. Failure log collection uses it to find runs that no step resolved. It is a separate interface, not a `ci.Driver` method, so external `ci.Driver` implementations keep compiling. Without it, failed scenarios still get a failure summary but only the run a step resolved has its logs collected. The GitHub Actions and GitLab CI reference drivers implement it.

Bump the pinned version when behaviour step vocabulary or `pkg/behaviourtest` APIs change.
