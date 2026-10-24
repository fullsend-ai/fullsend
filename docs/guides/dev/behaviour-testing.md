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
| `op` | `read_file`, `url_get`, `write_fixture`, `assert_env`, `assert_not_jwt`, `assert_file`, `assert_json`, `checkout_branch`, `http_probe`, `wait` |
| `args` | Op-specific; see below |
| `expect` | Optional. `fail` marks a step the scenario expects to fail, such as a fetch the sandbox blocks. The failure is still recorded for `the agent will fail to ...`, but it does not make the agent exit non-zero, so the run still succeeds. |

**`assert_not_jwt`:** `VAR` — succeeds when the sandbox variable `VAR` is set and does not hold a JWT (three base64url segments, the first starting `eyJ`). When its value is an absolute path to a readable file, as `INFERENCE_GATEWAY_TOKEN_FILE` is, the file must not hold one either. Use it to check that a credential reached the sandbox only as a placeholder.

**`wait`:** `SECONDS` — sleeps for a whole number of seconds (1 to 1200) between the ops before and after it, for example to outlast a credential's lifetime before probing again.

**`http_probe`:** `METHOD URL HEADER_ENV [BODY]` — `GET` or `POST` (body only with `POST`), sent through `node` from inside the sandbox with `Authorization: Bearer $HEADER_ENV`. `HEADER_ENV` must be `INFERENCE_GATEWAY_API_KEY` or `OPENAI_API_KEY`, the variables that hold OpenShell credential placeholders, so a scenario cannot send any other sandbox variable to a host of its choosing. `<gateway>` expands to `E2E_INFERENCE_GATEWAY_URL`. The HTTP status and the first 4 KiB of the response body are recorded in `behaviour-results.json` (the body is read only up to that limit); JWT-shaped substrings in the body are replaced by `<redacted-jwt>` and `body_had_jwt` records that one was there. The op succeeds only on a 2xx. Assert with `the agent's probe "<description>" returned HTTP <code>` and `... response contains|does not contain "<text>"`; a needle starting with `eyJ` also matches a redacted JWT. `features/runtime/inference-gateway-api-key.feature` runs the gateway's `api-key` mode (ADR 0138): `the test inference gateway is configured for the repository with an API key` commits `url` and `auth: api-key`, and the probe asserts that the echo upstream reports `STUB_KEY`, the gateway's own credential: a positive check, since the runner redacts the test key from the recorded results. It is gated on `inference-gateway-api-key` and skips when `E2E_INFERENCE_GATEWAY_TEST_KEY` is unset. The key must be authorised for the `echo` model only. The step sets it as the leased repository's `FULLSEND_INFERENCE_GATEWAY_API_KEY` secret, which the reusable workflow passes to the runner, and scenario cleanup deletes it, so the pool holds no standing key. Each gateway scenario starts with `the test inference gateway is available` (or `... available with an API key`), which skips before a repository is leased when the settings are unset, so runs without them spend no API calls on the gateway scenarios.

**`write_fixture`:** `dest_path, fixtures/...` — content lives in `e2e/behaviour/fixtures/`, embedded in the committed scenario script at `.fullsend/behaviour/current-scenario.yaml`.

**`checkout_branch`:** a single regex-validated branch name. Code and fix sandboxes cannot fetch git objects from the forge, so all remote access runs on the runner, against the host copy of the target repository and its `origin`; no sandbox policy is widened. The runner probes `refs/heads/<name>` with `git ls-remote --exit-code`. When the ref exists, the runner fetches it into a runner-private ref (`refs/fullsend-behaviour/heads/<name>`, deleted afterwards, so the runner's own branches are untouched), writes a `git bundle` of it, and uploads the bundle into the sandbox. The sandbox fetches the bundle and bases the branch on `FETCH_HEAD` (so the branch carries that ref's commits), then removes the bundle. When the ref is absent, the branch is based on the sandbox's current `HEAD`. Any other probe failure (network, auth) fails the op instead of silently falling back. Every failure message includes git's own stderr, from the runner and from the sandbox. The op then records one marker commit on the branch so the applier post-script has content to push — and so a wrongful push moves the target branch tip, giving `branch ... is unchanged` assertions something to detect. Deliberately a narrow capability — not a general shell op — and agent-agnostic: it works in any sandbox whose target repository has an `origin`, code or fix.

The `<issue>` placeholder expands to the scenario's issue number in `checkout_branch` args (only that op) and in the branch step definitions' branch names and head-branch patterns; order the `an issue` step before any step using it.

`write_fixture` content also expands three placeholders when the dummy-agent Given step reads the fixture: `<issue>` becomes the scenario's issue number, `<repo>` becomes the leased repository as `owner/name`, and `<pr_head_sha>` becomes the scenario pull request's head SHA. Use them for agent results that must name the scenario's own issue, repository or commit, such as a retro `target_repo` or a review result's `head_sha`, since all three change on every run. A review fixture needs `<pr_head_sha>` to request changes, because the review post-script turns a result for any other SHA into a stale-head notice. Put the dummy-agent step after `an issue` when a fixture uses `<issue>`, and after a pull request step when it uses `<pr_head_sha>`; otherwise the Given step fails. The head SHA is read once, so a later push to the pull request makes the fixture stale. The expansion applies only to `write_fixture` content: not to its paths, other ops' args, or `{{fixture:...}}` inlined into an agent file.

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
- **Runtime-specific (gated):** `Given the repository runtime is "<name>"` commits `runtime: <name>` to the leased repo's config for this scenario only (CleanupScenario restores `dummy` — slots are reused, so never set it any other way; the step refuses if the slot is not on `dummy` to begin with). The custom-harness step commits only a placeholder for a relative `agent:` path, which a real runtime cannot act on, so follow it with the agent step for the runtime under test (`And a pi agent "<name>" defined as:`, `And a codex agent "<name>" defined as:`, `And a claude agent "<name>" defined as:` — all commit the same file) and a docstring holding the full agent file (frontmatter + body) — `{{fixture:fixtures/<stage>/<file>.json}}` inlines a result fixture so the model has a concrete, deterministic file to write (the custom harness carries no post-script, so nothing validates it; the assertions are on the transcript and metrics). Then the scenario dispatches the harness and asserts on artifacts: `the run selected the "pi" runtime`, `the pi session transcript records at least one tool call` (the agent used a tool through pi; with security enabled the run refuses to start without the intact hook adapter, so the call was mediated by it — the step does not inspect hook output), `the run metrics report tokens`, and `the run used model "<id>" without a fallback` (the `model` in `metrics.json` is exactly that bare id, `fallback_from` is empty and a turn was recorded, so the alias reached a model the project serves without falling back). Such scenarios cost a real model run on the pool repo's repo-scoped Vertex WIF and must be tagged `@requires:capability:runtime-<name>` so they only run where the runner declares the capability; `make behaviour-test` declares `runtime-pi`, `inference-gateway` and `inference-gateway-api-key` by default (the two gateway capabilities reach no real model; see [Wiring it into the behaviour tests](e2e-testing.md#wiring-it-into-the-behaviour-tests)) (a `Makefile` variable, so a PR adding a gated scenario exercises it on its own `pull_request_target` run — the workflow file itself comes from `main`); `BEHAVIOUR_CAPABILITIES= make behaviour-test` skips them. See `features/runtime/pi.feature`. `features/runtime/pi-openai.feature` is the same shape on `openai/gpt-5.6-luna` with the `openai` provider instead of Vertex host files; it is gated on `runtime-pi-openai`, which is **not** declared by default because it needs an OpenAI organization mapped to the pool repositories plus their `FULLSEND_OPENAI_*` variables ([OpenAI Workload Identity](../infrastructure/openai-workload-identity.md)). `features/runtime/codex-openai.feature` is that same shape on the codex runtime — `And a codex agent "<name>" defined as:` for the agent, and `the codex output stream records at least one tool call`, which reads the tee'd `codex exec --json` stream (`output.jsonl`) rather than a session transcript. It is gated on `runtime-codex-openai` for the same reason, and codex has no direct Vertex path, so — unlike pi, whose `runtime-pi` scenario runs on every job — codex has **no default behaviour coverage at all** until that organization exists; its evidence until then is unit tests, recorded fixtures, local smoke runs and the on-demand `runtime-codex-gateway` scenario. `features/runtime/codex-gateway.feature` is the same codex shape on a `gateway/` model through the test inference gateway (ADR 0137), pinned to `gateway/gpt-oss-120b`, which the test gateway serves on Responses (codex speaks nothing else, and fullsend renders no model catalog for it, so the id is pinned). Its token step is `the run metrics report tokens unless the requested model is "gateway/gpt-oss-120b"`: on the test gateway that model's usage arrives after the finish chunk, so codex records 0 tokens, and the step requires a recorded turn instead (the workflow and tool-call steps prove the model answered). It is gated on `runtime-codex-gateway`, also undeclared by default, because it costs a real model run on the test gateway's own Vertex service account. `features/runtime/claude-gateway.feature` runs Claude Code on `gateway/claude-haiku-5-5` through the test inference gateway in the `oidc` mode (no model list, no Vertex host files) and asserts the runtime and token metrics. It is gated on `runtime-claude-gateway`, which is **not** declared by default, skips when `E2E_INFERENCE_GATEWAY_URL` is unset, and should run against a sandbox image built from main.
- **Per-agent (every run):** `Given the repository agents are configured with:` with a YAML docstring (`triage:\n  runtime: dummy`) sets runtime/model/effort/trigger on the leased repo's `agents:` entries (a name-only entry for a built-in, the sourced entry for a custom agent; only the settings given change) — validated the way `fullsend run` validates them — and CleanupScenario restores the pre-scenario `agents:` list. Pair it with `the repository runtime is "<real runtime>"` and pin every agent the scenario can dispatch (triage hands off to `code` via `ready-to-code`) back to `dummy`, then assert `the run selected the "dummy" runtime from "agents.triage"`, which also checks `runtime_source` in `metrics.json` ends with that entry — proof the per-agent entry decided, at dummy cost. The gated second scenario in the same file leaves the repo on `dummy` and puts one custom agent on pi with `model: haiku` from its entry (the harness says `opus`); `the run requested model "haiku" from "agents.<name>" and the provider reported a "haiku" model` checks `requested_model`, `override_source`, the reported `model` and `num_turns` in `metrics.json`. See `features/runtime/agent-settings.feature`.

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
The matched PR becomes the scenario's pull request, so the
[forge-state steps](#forge-state-assertion-steps) that follow read the
agent's PR rather than a fixture PR opened earlier.

`branch "<name>" has advanced` is the counterpart of `is unchanged`: it
passes only when the tip moved since it was recorded, for example because
an agent pushed to the branch.

For fail-closed paths there is a failure counterpart, asserted against
the run conclusion plus the post-script's failure comment on the
scenario PR:

```gherkin
Then the harness "fix" workflow fails reporting "Refusing to push"
```

Pick a stable fragment of the failure-comment contract (the category
label headline or a fixed detail phrase). No shipped scenario uses this
step yet; it is unit-tested.

### Forge-state assertion steps

`pkg/behaviourtest/steps/forge_state.go` asserts what agent post-scripts
leave on the forge. Agent behaviour scenarios in `fullsend-ai/agents` use
these steps through `go.mod`. Each step is a single forge read. Transient
errors are retried with the suite's retry helper (`cleanupRetry`), but no
step polls. Assert after `the harness "<agent>" workflow completes
successfully`, because by then the post-script has written to the forge.
"The issue" is the scenario's issue. "The pull request" is the
scenario's pull request: the one a Given step opened, or the agent's PR
once `the pull request head branch matches` has found it.

| Step | Passes when |
|------|-------------|
| `the issue has label "<label>"` | The issue carries the label |
| `the issue does not have label "<label>"` | The issue does not carry the label |
| `the pull request has label "<label>"` | The pull request carries the label |
| `the pull request does not have label "<label>"` | The pull request does not carry the label |
| `the issue has been closed as "<reason>"` | The issue is closed with state reason `completed`, `not_planned` or `duplicate` |
| `the pull request has been closed` | The pull request is closed, merged or not |
| `a comment on the issue contains "<text>"` | Some issue comment contains the text, such as a sticky marker like `<!-- fullsend:agent-status:` |
| `a comment on the pull request contains "<text>"` | Some pull request comment contains the text |
| `the pull request body contains "<text>"` | The PR body contains the text. `<issue>` expands, so `"Closes #<issue>"` checks the closing reference |
| `an issue with label "<label>" and title containing "<text>" exists` | An open issue other than the scenario's own carries the label and has a matching title, such as an issue an agent filed. `<issue>` expands; use it in the title so a leftover from an earlier run cannot match. The matched issue is closed at cleanup |

The Then wording `the pull request has been closed` is kept apart from
the When step `the pull request is closed`, which closes it, so godog
matching stays unambiguous.

Pull requests are read through the issue endpoints, which serve pull
requests as issues on GitHub. The existing pull request label and close
steps write through the same endpoints. GitLab numbers merge requests
apart from issues, so tag scenarios that use the pull request steps
`@skip:gitlab`. Only GitHub reports a close reason, so `has been closed
as` fails on other forges.

Two Given steps set up pull requests for these scenarios:

- `the write actor opens a pull request linked to the issue` opens the
  write actor's PR with `Closes #<issue>` in its body. Only the write
  actor can push a branch to the test repository. Order `an issue`
  before it.
- `a pull request that changes "<path>"` opens a PR, as the e2e
  installation identity, whose one commit writes `<path>` (for example
  `scripts/x.sh`) instead of the default `behaviour/pr.txt`. Its branch
  and PR are cleaned up with the scenario.

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

The suite runs scenarios in parallel by default. The default concurrency
equals the slots per org (12), not the total pool capacity, and
`GODOG_CONCURRENCY` overrides it. Each scenario gets its own `World` clone
and leases a unique `(org, test-repo-NN)` slot from the pool, so no
cross-scenario state is shared. The `behaviour-test` Make target includes `-race` to catch
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

The target runs `go test -tags playback -race -v -count=1 -timeout 45m ./e2e/behaviour/`. It uses only the `playback` build tag, so only `TestPlaybackSuite` (`behaviourtest.RunPlaybackSuite`) is compiled and `TestBehaviourSuite` is left out. Do not add `behaviour` to the tag list; that would run both suites. `RunPlaybackSuite` fixes the godog tag filter to `@playback` (`GODOG_TAGS` is ignored) and sets `PLAYBACK_RUNTIME=dummy-playback` for the install driver. Everything else is configured from the same environment as `make behaviour-test`: `ENVIRONMENT`, `BEHAVIOUR_SCM` / `BEHAVIOUR_CI` / `BEHAVIOUR_INSTALL_MODE`, `E2E_GCP_*`, `GODOG_CONCURRENCY`, `BEHAVIOUR_ARTIFACT_DIR`, and the role PEM / Cloudflare credentials the install factories need. `E2E_LOCK_TIMEOUT` does not apply, because neither suite acquires the org lock (see the slot leasing bullet under [Running the playback suite in CI](#running-the-playback-suite-in-ci)). `make behaviour-test` skips `@playback` scenarios, so the two targets never run the same scenario.

#### Running the playback suite in CI

The `playback` job mirrors the `behaviour` job in [`.github/workflows/e2e.yml`](../../../.github/workflows/e2e.yml), with these specifics:

- **Target and environment.** Run `make playback-test` with `shell: bash`, teeing output (for example to `playback-test.log`) so `pipefail` propagates failures. Pass the same `env:` block as the behaviour job: `BEHAVIOUR_SCM=github`, `BEHAVIOUR_CI=githubactions`, `BEHAVIOUR_INSTALL_MODE=per-repo`, `E2E_GCP_*`, `TEST_*_PEM`, and the `TEST_CLOUDFLARE_*` credentials. Point `BEHAVIOUR_ARTIFACT_DIR` at a playback-specific directory (for example `${{ runner.temp }}/playback-artifacts`). You do not need to set `GODOG_TAGS` or `PLAYBACK_RUNTIME`, because `RunPlaybackSuite` sets them itself.
- **Triggers, gate, and environment binding.** Use `needs: gate` for authorized `pull_request_target`, `merge_group`, and push to `main`; manual dispatch runs playback when `run_playback=true`. Bind to the same GitHub Environment (`stage` on push, `dev` otherwise) and set `ENVIRONMENT` to match. The job checks out and runs PR-head code with secrets, so the [CI Workflows security rules](../../contributing/ci-workflows.md#review-checklist-for-secrets-in-behaviourplayback-jobs) apply.
- **Slot leasing.** Neither suite reserves a whole org. `RunPlaybackSuite` and `RunSuite` lease individual `(org, test-repo-NN)` slots through the same shared install driver and allocator (see [Repo allocation via the shared slot driver](#repo-allocation-via-the-shared-slot-driver)), so the two suites and other concurrent runs can share an org without resetting each other's repos. Neither suite takes the exclusive `e2e-lock` (`e2etest.AcquireOrg`) or runs the broad startup `e2etest.CleanupStaleResources`. Abandoned slot repos are reclaimed by the allocator's [repo-slot GC](#repo-slot-gc). The playback and behaviour jobs still share a job concurrency group within each stage workflow run, because the STAGE pool is the single `halfsend` org with only 12 slots. Dev jobs use distinct groups and run concurrently across the numbered orgs.
- **Change detection.** Reuse the behaviour job's file filter (its `grep -qE` path list and the `push.paths` entries under the same `SYNC-WITH` comment). It already covers `e2e/behaviour/` (playback features, fixtures and results), `pkg/behaviourtest/`, `internal/runtime/` (the `dummy-playback` runtime), and `Makefile`. Keep the two filters in sync, and keep the behaviour job's fallback of running the tests when the file list cannot be fetched or may be truncated.
- **Cancellation.** The job inherits the workflow-level `concurrency` group and `cancel-in-progress` expression. A cancelled or killed run cannot run `t.Cleanup`, so its slot repos stay occupied until the [repo-slot GC](#repo-slot-gc) reclaims them, exactly as for the behaviour job. Gate post-test steps on `always()` rather than `success()` so that redaction still runs after a failure.
- **Artifacts.** Before uploading, redact the playback artifact directory with the base-branch `scripts/redact-behaviour-artifacts.sh`, run through `env -i` with the full secret list, as described in [Behaviour debug artifact redaction](../../contributing/ci-workflows.md#behaviour-debug-artifact-redaction). Upload only when `steps.redact.outcome == 'success'` and the suite step failed, with `if-no-files-found: ignore`. Use an artifact name distinct from `behaviour-artifacts-*`, for example `playback-artifacts-<pr-or-run-id>`.
- **Timeout.** Set `timeout-minutes: 45` to match the target's `go test -timeout` (see [CI timeout budgeting](#ci-timeout-budgeting-for-lazy-provisioning)).

To run playback from a trusted PR branch before merging, dispatch the existing E2E workflow with that branch as the ref:

```bash
gh workflow run e2e.yml --repo fullsend-ai/fullsend \
  --ref agent/7942-playback-test-target -f run_playback=true
```

This runs the branch's workflow and test target in the dev environment. The regular `behaviour` job is skipped for this playback dispatch. Artifact redaction uses the default-branch script.

In CI, the test runner mints cross-org `e2e` installation tokens via OIDC for GitHub API operations. Triage workflows on the pool org's `test-repo` mint same-org `triage` tokens from vendored reusable workflows; those require per-repo mint enrollment (`PER_REPO_WIF_REPOS`) on the hosted mint project. Pool `test-repo` repos are enrolled once by a GCP admin — not during CI install. Before `github setup`, the install driver resolves the repo-scoped inference WIF provider: it runs `fullsend inference status` and runs `fullsend inference provision` only when the provider is not healthy. Provisions are serialised across the process. A just-provisioned repo can briefly still report `not_provisioned`, so after a provision the driver re-reads `fullsend inference status` with a bounded backoff (up to 60 s of waiting) until the provider is healthy; the re-reads run after the provision lock is released, so a slow repo does not hold up other repos' provisions. The resolved provider is cached per repo name for the rest of the run, including after the repo is deleted and recreated, because the provider ID, its attribute condition and the Vertex AI grant are all keyed by `owner/repo`, not by repo ID (`Provisioner.ProvisionWIF` in `internal/dispatch/gcf/provisioner.go` creates the provider and the grant, and is the source of truth for these bindings). See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

### Repo allocation via the shared slot driver

`RunSuite` and `RunPlaybackSuite` build one shared install driver (`install.Factory` over an `install.PoolConfig`) for the environment's pool. On `dev` the pool is the derived orgs `halfsend-01` … `halfsend-12`, and on `stage` it is the single unsuffixed `halfsend` org, each with `test-repo-01` … `test-repo-NN` slots (`install.PoolLayoutForEnvironment`; `BEHAVIOUR_POOL_SIZE` sets the slots per org). The driver resolves one installation token per org (`e2etest.TokenForOrg`, minted once per org and kept for the run, since a token outlives the 45-minute job). It builds one forge client per org from that token. The `Given the enrolled test repository` step calls `install.LeaseDriver.AllocateLease(ctx)`, which:

1. Claims a free slot through the cross-org allocator (`slotpool.go`), blocking until one is free or ctx is cancelled. The claim *creates* the slot repo (the forge's `auto_init` provides the initial commit). A slot whose repo already exists belongs to another holder, possibly another suite. It is skipped for the whole of provisioning and is never reset or deleted. Orgs whose installation's rate-limit headers show the quota nearly spent are skipped until their reset, so leases move to orgs with quota left.
2. On the first lease in any org of a mint group, deploys the group's mint: a public preview mint on `dev` and the durable STAGE mint on `stage`. The driver packs the pool orgs into the fewest groups whose `WORKFLOW_HOST_REPOS` fits in 5,000 bytes, recomputed from the layout on every run (two groups for the 12 DEV orgs at 12 slots each, one for STAGE). Every mint's `--status-github-group` is the `mint-status` team of one pool org, picked at random once per job. Concurrent first leases in a group share one deploy. A failed deploy is torn down and retried by the group's next lease, and `Finalize` tears each deployed mint down once.
3. Installs fullsend into the claimed repo with the org's credential and mint URL. It deletes a leftover `{name}-fork` from an earlier holder of the slot, then runs `fullsend github setup` (after resolving the inference WIF provider when configured) and the post-install checks. If the install fails, the slot is released, deleting only this lease's own repo.
4. Returns an `install.RepoLease` with the org, repo, the org's token and client, and its group's mint URL, status group and workflow hosts. The step rebinds the scenario `World` to that org: `Org`, `RepoOwner`, `Token`, `MintURL`, `MintStatusGroup`, `MintWorkflowHostRepos`, and SCM/CI drivers rebuilt through `World.OrgDrivers` from the lease's client. Every step and the scenario's cleanup then act in the leased org with its credentials.

#### Public preview mints

The `dev` preview mints are deployed with `--public` (`PER_REPO_WIF_REPOS=*`), which broadens *caller* eligibility, not anonymous access. OIDC verification, the workflow-host allowlist (an explicit `--workflow-host-repos`, kept separate from callers per [ADR 0082](../../ADRs/0082-workflow-host-allow-list.md)), role permissions, and requested-repo and FOREIGN scope checks all still apply, and the status endpoint's GitHub-team authorization is independent of this mode. The accepted trade-off: the `fullsend-test-*` Apps are installed on every repo in the pool orgs, so an unrelated repo there that uses an approved workflow host could mint a token for *itself* through a live preview URL. The previews are temporary and the identities test-only. The [GitHub mint scope scenarios](#github-mint-scope-scenarios) keep the denials for another repository or org without the required FOREIGN grant and for unapproved workflow hosts. Grouped orgs share a preview hostname and its 60 requests/minute/IP native rate limiter, so validate 429s with live traffic rather than assuming the per-org traffic pattern still holds.

Godog concurrency stays independent of slot capacity. The default is the slots per org (12), and `GODOG_CONCURRENCY` overrides it. Capacity is every slot of the pool (144 on `dev`), and a concurrency above capacity only makes workers wait. The allocator logs each claim and release with its org/repo and that org's quota (`[slots] claimed halfsend-03/test-repo-05 (quota …)`). An exhausted pass logs the per-org breakdown: occupied, rate-limited until, denied, failed, e2e-lock excluded, and GC results. `Finalize` logs every org's last observed rate limit.

The After hook runs `CleanupScenario` (issues, PRs, ephemeral forks, hosting
repos) and then `LeaseDriver.DeallocateLease(org, repo)`. Cleanup changes the
slot repo and deletes repos by name, and the name may have been taken over by
another holder (a lessee that outlived the slot GC deadline, for example). So
the hook first sets `World.LeaseOwned` from the driver's `install.LeaseVerifier`
(`StillOwnsLease`, the same ownership check the release uses). `CleanupScenario`
checks it before every mutation and every cleanup retry, and the forge client
checks it again before resending a request after a rate-limit wait. A lost
lease, or one that cannot be verified, skips the mutation and logs why. A
`LeaseDriver` that does not implement `LeaseVerifier` is treated as
unverifiable. Local cleanup (mock servers, artifact directories) still runs,
and the lease is still released. `DeallocateLease` runs after
in-scenario debug collection has written workflow logs and agent artifacts
under `BEHAVIOUR_ARTIFACT_DIR`. It deletes the leftover fork only if it is a
real fork and the repo under the slot name is still this lease's (by
repository ID, else by the claim's description nonce), because a replaced slot
repo means the fork may belong to the new holder. The ownership check runs
after the fork is looked up, just before the first delete, and again before
any delete is resent. A fork it leaves is cleaned by the slot's next
`EnsureRepo`. It then deletes the slot repo only if it is still this lease's,
and frees the slot. A repo the release cannot delete stays occupied for
everyone until the [repo-slot GC](#repo-slot-gc) reclaims it. For a failed
scenario the After hook first runs `steps.CollectFailureLogs`. It saves the
logs of the repository's recent workflow runs that no step already saved,
which covers a wait that timed out or a step that failed before it resolved a
run. It always writes `debug-scenario-<name>-<suffix>/failure-summary.txt` with
the scenario error and each run's outcome. A run whose logs cannot be fetched
gets a `workflow-logs-unavailable.txt` stating why. The summary states when the
run listing may be truncated or the per-scenario fetch cap was reached. A
scenario that passes but whose repo deallocation fails also gets a failure
summary. Everything is redacted and written `0600`, like the in-scenario logs;
the redaction also masks the runner's `World.Token` and the values of
credential-named environment variables (for example `*_TOKEN`, `*_PAT`,
`*_KEY`), including in directory names. The next claim of that slot creates the
repo from scratch. Mint enrollment of the numbered *names* can remain
pre-provisioned; the GitHub repos themselves are ephemeral around a lease.
`Driver.Finalize` releases outstanding leases with an error, tears down every
org's mint, and stops the trace collector.

Concurrent ensures of the same repo are serialized via `singleflight.Group`, so only one goroutine runs the install while others wait. The numbered `test-repo-NN` repos do not need to be pre-provisioned in the pool orgs.

Scenarios that never lease a repo (for example the CLI mint status scenarios) still need a mint. "Given the suite mint address" therefore leases a slot first and uses that org's mint. Until a scenario leases, its `World` points at the pool's first org.

**Credential context separation:** The suite's e2e installation token and dispatch's per-repo `GITHUB_TOKEN` are distinct credential contexts with independent permission propagation graphs. After a pool repo is deleted and recreated, the suite can confirm the repo exists (via `GetRepo`), but it **cannot** observe or predict when dispatch-side collaborator permissions will be ready. Do not add `GetCollaboratorPermission` polling to the suite-side readiness checks — the suite's token resolves permissions through a different GitHub subsystem than dispatch's token. See the [package doc comment](../../../pkg/behaviourtest/drivers/install/doc.go) for details and the empirical evidence from [#6701](https://github.com/fullsend-ai/fullsend/issues/6701).

**Suite duration:** Because each lease of `test-repo-NN` pays create + `github setup` (not only the first use in a run), serial godog suites take longer than a shared-repo model. CI budgets **45 minutes** for the behaviour job (`timeout-minutes` and `go test -timeout`) to match.

Runner env (defaults shown):

```
BEHAVIOUR_SCM=github              # also: gitlab; future: forgejo
BEHAVIOUR_CI=githubactions        # also: gitlabci; future: tekton
BEHAVIOUR_INSTALL_MODE=per-repo
BEHAVIOUR_APP_SET=fullsend-test # app identities for both mint deployment and github setup; must match the supplied role PEMs
BEHAVIOUR_ARTIFACT_DIR=        # CI upload-artifact root for debug logs and run artifacts; temp dir when unset
BEHAVIOUR_TRACE_COLLECTOR=     # job-scoped OTel Collector: unset = on in GitHub Actions when BEHAVIOUR_ARTIFACT_DIR is set; true/false to force
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

### Shared org lock bridge (temporary)

The repo-slot allocator
([`slotpool.go`](../../../pkg/behaviourtest/drivers/install/slotpool.go),
[#8578](https://github.com/fullsend-ai/fullsend/issues/8578)) leases
individual `test-repo-NN` slots, so several suites can share an org. Suites
built from older branches still reserve a whole org with the exclusive
`e2e-lock` repository (`e2etest.AcquireOrg` / `e2etest.ReleaseLock`). Until
those runners drain, the allocator holds the org's `e2e-lock` in shared mode
for each lease
([`orglock.go`](../../../pkg/behaviourtest/drivers/install/orglock.go),
[#8620](https://github.com/fullsend-ai/fullsend/issues/8620)):

- `README.md` keeps a UUID, the lock generation. Legacy runners treat it as
  another holder's run ID, so they neither release the lock nor delete it as
  invalid.
- `SHARED_LOCK` is the shared-mode flag. A lock without it is a legacy
  exclusive lock, and new runners skip that org.
- `holders.json` records the generation, the holders, and whether admission
  is closed. It changes only through `forge.Client.UpdateFileIfMatch`, a
  write conditional on the file's blob SHA. Joins and leaves reread and
  retry after a conflict. The last holder closes admission in the same
  write that removes it, then deletes the lock. A release against a lock of
  another generation, or a legacy lock, changes nothing.
- A lease joins only a shared lock younger than 15 minutes, and otherwise
  looks for another org. Any lock older than 30 minutes is reclaimed, the
  same rule legacy runners apply.

**Residual risk:** Legacy runners reclaim any lock older than 30 minutes by
creation age alone, without checking holders. A lease admitted near the
15-minute mark whose scenario plus cleanup runs past the lock's 30th minute
can therefore share its org with a legacy suite that resets `test-repo-NN`
repositories. The admission window reduces this risk but does not rule it
out. Join logs record the lock age at admission. Leave logs record how long
each holder ran and warn at 25 minutes and beyond 30 minutes.

A second, narrower window exists while a lock is created. The new repository
briefly shows the default non-UUID README, which a legacy runner deletes as
invalid before recreating the lock with its own run ID. The allocator rechecks
the repository ID right before its initial commit, and again after every wait
inside that commit (repository-initialisation, rate-limit and stale-head
retries, which can add up to several seconds), and does not join a lock that
was replaced. The platform has no commit conditional on the repository ID, so
a replacement between a check and the next request, or among the requests the
commit sends back to back, can still let both suites use the org.

**Removal:** Delete the bridge (and the shared files in `e2e-lock`) once no
runner can still take the exclusive lock. That means every open PR branch
and the merge queue contain the allocator wiring, and the last legacy run
has finished or been reclaimed. Repo-slot GC (below) uses its own persistent
coordination repository, not this temporary lock.

### Repo-slot GC

A runner that crashes, or whose release fails, leaves its `test-repo-NN`
repository behind, and every allocator treats that slot as occupied. The
allocator reclaims such slots itself
([`slotgc.go`](../../../pkg/behaviourtest/drivers/install/slotgc.go),
[#8621](https://github.com/fullsend-ai/fullsend/issues/8621)). DEV and the
single unsuffixed STAGE org use the same code path.

- **Trigger.** A claim pass that leases nothing counts, per org, the slots it
  found occupied by another lease's repository. When an org reaches two full
  passes' worth (2 × slots per org, 24 for a 12-slot org), the pass collects
  that org before it reports no capacity, and the count starts again. An org
  inside a rate-limit window is not collected, or probed, until the window
  ends. When GC frees a slot, `Acquire` claims again without waiting for the
  next poll.
- **Candidates.** GC inspects only the slot names derived from the pool
  layout. It reclaims a repository only when the description is the
  test-ownership marker (`fullsend behaviour test slot lease=` plus a 32-digit
  hex nonce) and the repository ID and creation time are known. The repository
  must also not be archived and must be at least **1 hour** old. Anything
  else, including a failed or throttled lookup, stays occupied. The reason
  appears in the capacity error and in the `[slots] gc` log lines.
- **Age bound.** The hour is the 45-minute limit of the suites that hold
  leases plus a 15-minute margin. That limit is the Makefile `go test -timeout
  45m` and the `timeout-minutes: 45` of the `behaviour` and `playback` jobs in
  `e2e.yml`. The first panics the test binary and the second kills the runner,
  so a slot repository, which never outlives the process that claimed it,
  cannot be live after 45 minutes. A Godog context deadline would not stop a
  step, so it is not the bound. The margin covers requests still in flight
  when a job is killed, and clock skew. `TestSlotReclaimAgeCoversJobLimits`
  fails if either limit rises above 45 minutes. Raise the bound in
  `slotgc.go` together with them, and do not run the suites with a longer or
  disabled `-timeout` against shared orgs.
- **One collector per org.** The lease is `collector-lease.json` in the
  org's persistent `behaviour-slot-gc` repository. It changes only through
  `forge.Client.UpdateFileIfMatch`. A collector skips the org while another
  collector's lease is unexpired, or within 5 minutes after the last
  collection ended. It scans the slots read-only, and only when it finds a
  candidate does it take a 10-minute lease conditional on the SHA it read. A
  collector that loses that write does nothing.
- **Before each delete,** the collector rereads the lease. It acts only
  while the file is still exactly what it wrote and more than 4 minutes of
  the lease remain. It then rereads the repository and deletes only if the
  ID, description and creation time are unchanged. The delete is not resent
  after an ambiguous failure. Before a resend after a rate-limit wait, both
  checks run again. Release is conditional on the SHA the collector wrote, so
  a collector whose lease was taken over can neither use nor release its
  successor's lease. A crashed collector's lease is taken over once it
  expires.
- **Diagnostics.** `CapacityError` carries `Reclaimed` and a per-org `GC`
  outcome, and `GCDiagnostics()` returns the last outcome per org, next to
  `QuotaDiagnostics()`.

**Residual risk:** GitHub deletes repositories by name only, so the final
identity check and the delete are one round trip apart. A collector paused
for longer than its 4-minute safety margin inside that window could still
delete a repository recreated after a successor reclaimed the slot.

### Job-scoped trace collection

The tested Fullsend workflows run on separate GitHub-hosted runners, so the suite gives them a single place to send traces. When the trace collector is enabled, the CF mint install drivers (`NewRepoPoolCFMintPreviews`, `NewRepoPoolCFMintStage`) set it up during suite setup. No workflow changes are needed:

1. Generate a random bearer token for the job. The token is registered with the process secret redactor. It is never placed on a command line, in a file or on stdout. In particular it is not announced with `::add-mask::`: the step log is tee'd into the artifact directory, and a suite killed by the timeout never runs the scrub, so that line would leave the raw token in the uploaded log.
2. Start the OpenTelemetry Collector (`otelcol-contrib`) with Podman. Its OTLP/HTTP receiver is published on `127.0.0.1` of the CI worker and requires the token through the `bearertokenauth` extension. The file exporter writes received traces as OTLP/JSON lines, one file per trace ID (`/traces/trace-<trace ID>.jsonl`). The file exporter can only route by a resource attribute, so the traces pipeline first copies each span's trace ID into a temporary `bt.trace_id` span attribute (`transform` processor) and promotes it to a resource attribute (`groupbyattrs` processor). Spans of one trace that arrive in different OTLP requests are appended to the same file, and different trace IDs never share a file.
3. Probe the receiver. Startup fails unless a request without a token and a request with a wrong token are both rejected and the job token is accepted.
4. Start a Cloudflare quick tunnel (`cloudflared` container) that gives the receiver a public HTTPS endpoint under `trycloudflare.com`, so the token is protected by TLS in transit.
5. On every `AllocateLease`, set the leased repository's `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` variable to the tunnel endpoint (path `/v1/traces`) and its `OTEL_EXPORTER_OTLP_TRACES_HEADERS` secret to `Authorization=Bearer%20<token>`. The reusable workflows already forward both to `fullsend`. Deleting the repository at deallocation removes both.

`NewRepoPoolCFMintPreviews` starts the collector before it deploys the preview mint and points the mint at it too: it writes `Authorization=Bearer%20<token>` to a private (`0600`) temporary file, runs `fullsend mint deploy` with `--otlp-traces-endpoint <tunnel endpoint>` and `--otlp-headers-file <file>`, and deletes the file once the deploy returns. The token reaches the Worker only as the version-scoped `MINT_OTLP_HEADERS` secret (see [`mint deploy` tracing](../../cli/mint.md#tracing)). Workflow and mint spans share trace IDs through `traceparent` propagation, so each `trace-<trace ID>.jsonl` file holds the mint's server and stage spans next to the workflow spans that called it. If the mint deploy fails, the collector is stopped before the factory returns. `NewRepoPoolCFMintStage` does not configure its mint: the stage mint is a durable Worker shared across runs, so it is not pointed at a per-job collector.

`Driver.Finalize` stops the tunnel, then stops the Collector with a grace period so it flushes, and removes both containers. It then copies every `trace-<trace ID>.jsonl` file and `collector.log` to `otel-collector/` under `BEHAVIOUR_ARTIFACT_DIR` (mode `0600`) and overwrites every literal occurrence of the token in files under `BEHAVIOUR_ARTIFACT_DIR` with a mask of the same length. The scrub matches raw bytes only, so it does not look inside compressed or archived files. Existing files and symlinks at the destination are replaced, never written through. If the traces cannot be copied, the source files are kept and the next `Stop` call retries the copy. If the collector fails to start and its cleanup also fails, `Finalize` retries that cleanup. The overwrite is done in place, so a file that did capture the token keeps its size and offsets. Cleanup runs under its own bounded timeout, and if removing a container fails it is retried by the next `Stop` call. Finalize runs in `t.Cleanup` within the `make behaviour-test` step, which ends before the workflow's redaction and `behaviour-artifacts-*` upload steps, whether the suite passed or failed. The trace files then pass through the redaction step like any other artifact. The trace files work with `hack/upload-traces.sh` (see [tracing.md](tracing.md)) if you want to replay them into a trace backend.

`BEHAVIOUR_TRACE_COLLECTOR` controls the collector. When unset, it runs only in GitHub Actions and only when `BEHAVIOUR_ARTIFACT_DIR` is set. Set it to `true` to run the collector locally (requires Podman and `BEHAVIOUR_ARTIFACT_DIR`), or `false` to turn it off. Trace collection is diagnostic: if the collector cannot start (no Podman, image pull or tunnel failure), the suite logs a warning and runs without it. If a repository cannot be configured, the suite logs that and continues. A suite killed by the `go test` timeout skips `t.Cleanup`, so no trace files are saved for that run.

To check the per-trace routing against the pinned Collector image, run `BT_OTELCOL_INTEGRATION=1 go test ./pkg/behaviourtest/drivers/install -run PerTraceFilesWithRealCollector` (requires Podman). The test sends interleaved spans of two trace IDs across several export requests and verifies that every span is preserved and each trace ID has its own file. The test is skipped otherwise.

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

Scenarios that fork as a human-like test actor (`owned by the outsider`) additionally require:

- **The actor can fork the leased base into its own account.** The base is public, so a user with no collaborator access can fork it.
- **The actor's PAT can create, write to, and delete repositories in its own account**, because the actor commits to its fork, opens the PR, and `CleanupScenario` deletes the fork as that actor. A classic PAT needs the `public_repo` and `delete_repo` scopes.

### Fork lifecycle

| Resource | Lifecycle | Cleanup |
|----------|-----------|---------|
| Fork repo | Per-scenario (`{RepoName}-fork`); created on demand | Deleted by `CleanupScenario` |
| Fork branches | Per-scenario | Deleted by `CleanupScenario` (before repo deletion) |
| Fork PRs | Per-scenario | Closed by `CleanupScenario` |

Fork repos are **ephemeral**: created when the `Given a fork` step runs and deleted by `CleanupScenario` after the scenario completes. Fork PRs are opened against the base repo (not the fork). `CleanupScenario` closes them via `CloseIssue` on the base repo, deletes the head branch on the fork repo, and then deletes the fork repo itself. Do **not** mint-enroll fork names — forks are PR sources only; mint stays on the enrolled base.

A fork **owned by a test actor** lives in that actor's own account, not the pool org. The forge names it, by default after the base repo (for example `fstest-outsider/test-repo-07`), and adds a numeric suffix when the actor already has a repo with that name. The logical fork name in Gherkin is not used. `World.ForkSCM` records the actor's driver, and fork-side operations go through it: branch creation, commits, and the branch and repo deletes in `CleanupScenario`. The e2e installation token has no access to a personal account. The pool's repo ensurer only deletes leftover `{name}-fork` repos inside the org, so a fork whose cleanup failed stays in the actor's account until someone deletes it.

### Background step usage

Fork scenarios share a common `Background:` block that sets up the enrolled test repository and the fork:

```gherkin
Background:
  Given the enrolled test repository
  And a fork "test-repo-fork" of the enrolled test repository
```

The `Given a fork` step remaps the logical name as above and is idempotent for that actual fork repo. Each scenario then creates its own branch and PR within the fork.

Two fork steps take an optional actor suffix. The actor is one of `write actor`, `triage actor`, or `outsider`, the same names the actor steps use (see [Test actor account permission scope](#test-actor-account-permission-scope)). Without the suffix, both steps act as the e2e identity:

- **`a fork "<name>" of the enrolled test repository owned by the <actor>`** forks the leased base into the actor's own account. See [Fork lifecycle](#fork-lifecycle).
- **`a fork pull request is opened by the <actor>`** prepares the head branch through the fork owner's driver and opens the PR as the actor. The actor is then both the PR author and the sender of the `opened` event.

### Fork PR behaviour contract

The `fork-dispatch.feature` file defines the canonical fork-PR behaviour contract for `harness-dispatch`. Each CEL port PR ([#2896](https://github.com/fullsend-ai/fullsend/issues/2896)–[#2901](https://github.com/fullsend-ai/fullsend/issues/2901)) should follow this contract when adding fork-PR rows to the agent's harness behaviour feature file.

| Scenario | Expected | How tested |
|----------|----------|------------|
| Fork PR matches CEL trigger; authorized actor | Agent runs via `harness-dispatch`; workflow completes | Positive dispatch + artifact assertion |
| Kill switch active (`kill_switch: true`) on fork event | Empty matrix, exit 0 | Separate scenario; `the kill switch is active` step + assert agent did not run |
| Disabled harness (`enabled: false`) on fork event | Empty matrix, exit 0 | Disabled harness in positive scenario; assert agent did not run |
| Fork PR `synchronize` + label dispatches harness | Harness dispatched exactly 1 time; workflow completes | Separate scenario with sync commit + label |
| CEL `is_fork` exclusion (`!event.state.change_proposal.is_fork`) | Empty matrix, exit 0 | Harness with `is_fork` guard in positive scenario; assert agent did not run |
| Fork PR matches CEL `trigger` on `pull_request_target`; unauthorized actor ([ADR 0054](../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)) | Empty matrix, exit 0 — no agent run | `platform/trigger-auth.feature`. The outsider opens a PR from its own fork; assert the harness dispatch completed with an empty matrix, the agent did not run, and it was dispatched exactly 0 times, then a label as the positive control |

**Kill switch vs disabled harness:** These are distinct mechanisms. The **kill switch** (`kill_switch: true`, settable in either `config.yaml` or `config.base.yaml`) is a global emergency stop that blocks *all* harness dispatch for the repo — tested in its own scenario because no positive harness can run alongside it. A **disabled harness** (`enabled: false` per agent entry) only prevents that single agent from running — tested as a piggyback negative assertion in the positive-path scenario.

**Consolidation pattern:** To conserve parallel execution slots, add negative-path harnesses (disabled agent, CEL exclusion) alongside the positive-path harness in a single scenario rather than creating separate scenarios. The positive harness wait acts as the settle window for negative assertions (piggyback pattern — see `negativeSettleDuration` in `dispatch.go`). The kill switch scenario cannot be consolidated because it blocks all harnesses.

**Unauthorized-actor denial:** The row lives in `e2e/behaviour/features/platform/trigger-auth.feature`, the home for platform trigger authorization scenarios, rather than in `fork-dispatch.feature`. The dispatch gate (`harnessdispatch.IsEventAuthorized`, then the per-harness `role:` threshold from `IsHarnessAuthorized`) authorizes label-added events without any role. So the denial can only be exercised through a transition that needs one — for the `fork-auth-probe` harness (`role: triage`), the `triage` threshold: the PR `opened` event, sent by the outsider when it opens the PR from its own fork. The harness trigger matches both `opened` and a label, and the scenario orders its steps as follows:

1. The denial assertions (`the harness dispatch completes with an empty matrix`, then `agent did not run`, then `dispatched exactly 0 times`) run before the label is added. The first is the one that checks the required outcome: the other two also pass when the dispatch job errored or when no run was created, because neither counts a run without an agent job. `harness-run` uses a `cancel-in-progress` concurrency group per agent and PR, and the dispatch count excludes cancelled jobs. A leaked `opened` dispatch that the label run cancelled would therefore not be counted.
2. The label, added afterwards by the e2e identity, must dispatch. This positive control shows that the harness is registered and its trigger matches the PR, so the denial cannot pass just because the harness never loaded.

### Dispatch step reference

**`a disabled custom harness "<name>" with:`** — Registers the harness YAML under `.fullsend/harness/<name>.yaml` and adds an agent entry with `enabled: false` to the repo's `config.yaml`. Use this step for negative dispatch assertions where a single agent should be excluded while other agents in the same scenario continue to run. This is *not* the kill switch; for the global emergency stop that blocks all harnesses, use `the kill switch is active`.

**`the harness dispatch completes with an empty matrix`** — Waits for the harness workflow run(s) created since the last trigger step, then requires every run that was not cancelled or skipped (a skipped run, such as the shim run for a bot comment, ran no job) to have completed with its `Harness dispatch` job succeeded and no `Harness run (<agent>)` job scheduled. It fails if the dispatch job failed or an agent was scheduled, and keeps waiting while no run exists yet or a run is still in progress, so a denial is never inferred from a missing or failed run. Pair it with `agent did not run` and `dispatched exactly 0 times` for denial scenarios. It needs a CI driver that implements the optional `ci.EmptyMatrixWaiter`; the GitHub Actions driver does.

**`the harness "<agent>" run was skipped by its pre-script`** — Downloads
the `fullsend-<agent>` artifact of the workflow run the scenario is tracking
and requires two things. First, the host-written `run-telemetry.jsonl` must
record `fullsend.prescript.skipped=true` on its root `run` span. Second, no
agent output (`agent-result.json`, or the dummy runtime's
`behaviour-results.json`) may be present. A skipped run still uploads its
artifact and its workflow still succeeds, so
`the harness "<agent>" workflow completes successfully` passes either way.
Use this step after that one, which records the run. On failure it reports
what it found: `fullsend.prescript.skipped=false`, a missing attribute (the
harness has no pre-script), missing telemetry, or the agent output files
that were present.

**`a changes-requested review is submitted on the pull request`** — Submits a `REQUEST_CHANGES` review on the scenario's pull request as the e2e identity, a bot that is not the registered review bot, and restarts the trigger time. GitHub only. GitHub refuses a changes-requested review on the reviewer's own pull request, so a test actor must open it (`the write actor opens a pull request`).

**`the kill switch is active`** — Sets `kill_switch: true` in the repo's `config.yaml` (the overlay), causing `Dispatch` to return an empty matrix for *all* agents. Use this step in a dedicated scenario where no harness should run. Because the kill switch blocks everything, it cannot share a scenario with a positive-path harness.

**`the kill switch is active in config.base.yaml`** — Sets `kill_switch: true` in `config.base.yaml` (the central/base layer) instead, leaving `config.yaml` unset. Use this step to verify that a base-file kill switch halts dispatch the same way an overlay one does, since the two files are read independently.

## Forge operational constraints

When modifying behaviour test repo provisioning, fork handling, or workflow dispatch, be aware of these constraints. They are not enforced by the compiler or linter — violations surface as cryptic API errors or silently dropped events in CI.

### `auto_init` creates an initial commit

The forge's `CreateRepo` uses `auto_init`, which creates an initial commit containing a README. Do **not** call `CreateFile("README.md")` (or any file that `auto_init` already provides) on a newly created repo — the GitHub API returns a 422 because the file already exists in the initial commit.

If a scenario needs to seed additional files, use a filename that does not collide with the `auto_init` commit (e.g., `seed.txt` instead of `README.md`), or check for existence first.

Reference: the slot allocator's `Claim` in [`slotpool.go`](../../../pkg/behaviourtest/drivers/install/slotpool.go), which creates each slot repo with `CreateRepo`.

### An existing slot repo is never deleted during provisioning

A slot repo that already exists is another holder's lease, possibly a concurrent suite's. A `CreateRepo` "already exists" on a slot name therefore means *occupied*. The allocator moves on to another slot and never deletes the repo to free the name. The only exception is a repo carrying one of the allocator's own pending claim nonces, which it adopts (see `slotpool.go`). The ensurer never creates, deletes or resets a slot repo. It waits for the claimed repo to become visible (`awaitCreation`) and fails the allocation if it never does. Slot repos abandoned by crashed runners are reclaimed only by the [repo-slot GC](#repo-slot-gc), under its age and ownership checks.

GitHub's reads are not monotonic. A `GetRepo` right after a create can 404, and a `GetRepo` right after a delete can still return the deleted repo. So `awaitCreation` polls with backoff. The release path checks repository identity before and after its `DELETE` instead of trusting a single read.

Reference: [`awaitCreation`](../../../pkg/behaviourtest/drivers/install/ensure.go), [`deleteLeftoverFork`](../../../pkg/behaviourtest/drivers/install/ensure.go), and the allocator's `Release` in [`slotpool.go`](../../../pkg/behaviourtest/drivers/install/slotpool.go).

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

Current budget: **45 minutes** for both the CI job timeout and `go test -timeout`. If adding scenarios that lease additional repos (or increase reuse of the 12-slot pool), verify that the total provisioning overhead plus test execution time fits within this budget. The [GitHub mint scope scenarios](#github-mint-scope-scenarios) and the [GitHub bot-role lookup scenarios](#github-bot-role-lookup-scenarios) lease one repo per Examples row. Adjust both values together — a `go test -timeout` higher than the CI `timeout-minutes` means the Go process is killed mid-test with no artifact collection. The same rule applies to the `playback-test` target (also 45 minutes) and to any CI job that runs it.

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

`doEnsure` always installs and settles. Every lease starts from a repo its claim just created, so there is no re-vendor path that skips the settle wait: the settle step runs on every ensure.

## GitHub mint scope scenarios

`e2e/behaviour/features/platform/mint-scope.feature` covers the GitHub per-repo mint request contract with real GitHub Actions OIDC tokens. It sends requests to the suite's own mint: the preview mint on `dev`, the stage mint on `stage`. A `Scenario Outline` holds the matrix, and each Examples row is one request and its expected result:

| Column | Values |
|--------|--------|
| `forge` | `github` (the only supported forge; the feature is tagged `@skip:gitlab`) |
| `role` | A `fullsend-test` role: `triage`, `coder`, `review`, `retro`, `prioritize`, `fullsend` |
| `repos` | `shim repo` (the leased repository), `other repo` (another repository of the same org, see below) or `*` |
| `level` | `read` or `write` |
| `workflow host` | `approved` or `unapproved` |
| `outcome` | `minted`, `scope-denied` or `host-denied` |
| `target repo` | `shim repo` or `none` |
| `selection` | The token's `repository_selection` (`selected`), or `none` |

Every role mints at both levels for its own repository. Wildcard and other-repository scopes are denied (`scope-denied`), and an unapproved workflow host is denied (`host-denied`). Out of scope: the deprecated per-org installation mode, the infrastructure-only `e2e` role, cross-organization minting, and GitLab or Forgejo minting.

### Probe topology

The probe adds and changes no committed Fullsend workflow, and runs no harness or dummy runtime. For each row the steps:

1. Create a temporary, public host repository in the pool org and commit the reusable probe workflow (`pkg/behaviourtest/steps/mint_probe_workflow.yml`) to it. The approved host is `test-repo-NN-mint-host`. Both CF mint drivers list that name in the mint's `WORKFLOW_HOST_REPOS`, next to the pool repos (see [Install driver (unified)](behaviour-drivers.md#install-driver-unified)). Unapproved-host rows also create `test-repo-NN-mint-unapproved`, which no list names.
2. Push a shim workflow to a temporary branch (`behaviour/mint-probe-<nonce>`) of the leased repository. On that push, the shim calls the probe workflow twice in one run and passes the repository's `FULLSEND_MINT_URL` variable to both jobs.
3. Wait for that run, download each job's result artifact and classify it.

The leased repository is the OIDC `repository` claim (the shim) and the host repository is the `job_workflow_ref` claim, so the two are always different repositories. The probe reports both claims, and the outcome step checks them. `other repo` is the approved host itself: it is in the same org and covered by the role's App installation, so a denial cannot come from a missing repository. `CleanupScenario` deletes the shim branch and the host repositories, and the leased repository is deleted when it is deallocated.

### Telling denials from failures

Each probe job requests one token with the job's OIDC token, then masks and revokes any token it gets. It uploads a compact JSON result: the outcome (`minted`, `denied` or `error`), the mint's HTTP status and error message, the two claims, and a minted token's `repository_selection`, granted repositories and granted permissions. No token is kept. The job exits 0 whatever the mint answers, and the upload requires the result file, so a missing result fails the run.

The steps report a policy denial separately from an infrastructure failure. These are infrastructure failures, and none can satisfy an expected denial:

- an OIDC failure: no OIDC token was obtained, or the probe could not verify it (see below);
- a transport failure: the mint request did not complete;
- a server failure: an unexpected HTTP status, such as a 422 for a missing App permission, or a 5xx;
- a malformed result: a 200 without a usable token;
- a configuration failure: no `FULLSEND_MINT_URL`;
- a workflow failure: the run did not succeed;
- a result-artifact failure: the artifact is missing or holds an unknown outcome.

A scope denial must be a 403 with one of the mint's repos-scope messages (`validateReposScope` in `internal/mintcore`). Any other 403, for example `role not allowed`, is a misconfiguration, not a scope denial. A workflow-host denial is a 401. The mint answers `authentication failed` both when the workflow host is not approved and when OIDC verification fails. To rule out the latter, the same run makes a **control** request: same role and level, the leased repository only, through the approved host. The control must mint a `selected` token for the leased repository with the role's canonical permissions, or the row fails whatever the subject request returned.

The control rules out an OIDC failure common to both requests, but not one specific to the subject's token. So each probe job also verifies its own OIDC token before it calls the mint: the RS256 signature against GitHub's published signing keys, the issuer, the `fullsend-mint` audience, and `exp`/`nbf`. A token that fails is reported as an OIDC failure and the mint is not called. The result carries `oidc_verified`, and a 401 `authentication failed` counts as a host denial only when it is `true`. The mint's 401 stays generic on purpose, so no mint-side denial reason is needed. The step log prints only the outcome, error class, HTTP status and `oidc_verified`; the mint's message is kept in the artifact alone.

A minted token's granted permissions are compared with the role's canonical set at the requested level. Permissions the mint may drop while an installation catches up (`coder`'s `packages:read`) may be absent. Nothing else may differ. The expected sets duplicate `canonicalRolePermissions` and `optionalRolePermissions` in `internal/mintcore/github.go`, because `pkg/behaviourtest` must not import mintcore (see [Version pinning](#version-pinning-for-fullsend-aiagents)). Change both together. `TestMintScopeFeature_RowsMatchSteps` checks that every Examples row matches the step patterns and that every role mints at both levels.

### Budget

Each row is a scenario that leases a pool repository, so the 18 rows add 18 leases (about 3–5 minutes of provisioning each) to the [CI timeout budget](#ci-timeout-budgeting-for-lazy-provisioning). Each probe run is short: two parallel jobs that each make one HTTP request. If the suite outgrows the budget, grow the pool through `DefaultPoolSize` (`pkg/behaviourtest/drivers/install/driver.go`). Update the pool-sized infrastructure in the same change, for example `POOL_REPOS` in the inference gateway setup script (see [e2e-testing.md](e2e-testing.md#who-runs-it-and-when)). Then rerun that infrastructure before the larger pool is used.

## CLI mint status scenarios

`e2e/behaviour/features/platform/mint-status.feature` checks that an operator who knows only the mint address and has an authorized GitHub account gets the mint's status through the CLI: `fullsend mint status --mint-url <mint address>`. The target is the suite's own mint: the preview mint of the leased org's mint group on `dev`, the STAGE mint on `stage`. Both CF mint drivers deploy it with `--status-auth oidc,github --status-github-group <org>/mint-status`, where `<org>` is one pool org picked once per job, and the write actor is the only member of that team (see [Mint status team](e2e-testing.md#mint-status-team)). The steps get the address from the install driver through the lease: the scenarios lease a pool slot (`givenEnrolledTestRepository`) and read `RepoLease.MintURL`. The lease also carries the status group and the mint's workflow hosts (`World.MintStatusGroup`, `World.MintWorkflowHostRepos`), which the failure diagnostic and the facts check use instead of deriving them from the scenario's org. The optional `install.MintURLReporter` interface is the fallback for drivers that do not lease slots.

The steps run the suite's CLI binary (`World.CLIBinary`) with only `--mint-url`, never `--project`. The CLI gets a clean environment. It inherits only `PATH`, `TMPDIR`, the proxy variables and `SSL_CERT_FILE`/`SSL_CERT_DIR`. It gets a fresh `HOME` and gh config directory, and `GH_TOKEN` set to the credential under test. The GitHub Actions OIDC variables, which the behaviour job has because it declares `id-token: write`, are withheld, so the CLI cannot authenticate with OIDC before it falls back to the user token. GCP project and credential variables, `GITHUB_TOKEN` and `FULLSEND_MINT_URL` are withheld too. The status facts are read from the CLI's output, and nothing calls `/v1/status` directly:

- authenticated via `GitHub`, in the step line and the summary;
- the URL the CLI reports is the address it was given;
- no caller organization, which only OIDC status auth reports;
- every `fullsend-test` role is listed, and the summary's role count matches the list;
- the workflow host repositories are exactly the mint group's configured hosts (or, for a driver that reports none, all in the scenario's org), and the scenario's own leased repository is one of them.

A failure has one of four classes, and the step error starts with the class:

| Class | Meaning |
|-------|---------|
| `authentication` | The mint rejected the credential (the CLI's `authentication failed for /v1/status`) |
| `transport` | The CLI could not reach the mint, or did not finish within 3 minutes |
| `deployment` | The install driver reported no mint URL, or the address answered but not as a mint's status API (an unexpected HTTP status or an unparsable response) |
| `assertion` | The CLI succeeded but reported the wrong facts, printed the credential, or a failure was expected and did not happen |

The mint answers a GitHub token it does not accept with the same 401 in three cases: the token is invalid, its user is not an active member of the status group, or the mint was deployed without GitHub status auth. A rejected write actor credential is therefore an authentication failure whose message names all three causes. A missing or misconfigured status group can never pass.

The Scenario Outline holds a negative control for each failure class, and none of them may pass as a success. An `invalid credential` must fail as `authentication`. The `unreachable mint address` (`https://127.0.0.1:1`) must fail as `transport`. The `suite mint address with a non-mint path` (the suite mint address plus `/not-a-mint`, which the mint answers with 404) must fail as `deployment`. The deployment control sends the write actor credential only to the suite's own mint.

The steps register the credential with the secret redactor. They store only redacted CLI output and save it as `mint-status-<scenario>-*/cli-output.txt` under `BEHAVIOUR_ARTIFACT_DIR`. The last step of each scenario fails if the raw CLI output held the credential, or if any file under `BEHAVIOUR_ARTIFACT_DIR` holds the write actor's PAT. In CI the PAT is a repository secret, so GitHub Actions also masks it in the job log.

## GitHub bot-role lookup scenarios

`e2e/behaviour/features/platform/bot-role-lookup.feature` covers the GitHub bot-role lookup ([ADR 0107](../../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md)): the mint's `POST /v1/bot-role`, called with a real GitHub Actions OIDC token, against the suite's own mint. It reuses the [mint probe topology](#probe-topology): the steps commit a reusable probe workflow (`pkg/behaviourtest/steps/bot_role_probe_workflow.yml`) as `.github/workflows/bot-role-probe.yml` to the slot's approved host `test-repo-NN-mint-host`, then push a shim to a temporary branch (`behaviour/bot-role-probe-<nonce>`) of the leased repository. The suite's mints accept any workflow basename (`ALLOWED_WORKFLOW_FILES` is `*` on the preview and stage mints). Each Examples row is one identity:

| Identity | Subject lookup | Expected outcome |
|----------|----------------|------------------|
| `registered:<role>` | `<app set>-<role>[bot]`, where the app set is `BEHAVIOUR_APP_SET` (default `fullsend-test`, see `install.AppSet`) and the role a `fullsend-test` role | `recognized` with that role |
| `unregistered-e2e` | The suite's own e2e App, discovered at runtime from the suite's GitHub credential. It must be a `[bot]` login and none of the registered logins, or the row fails as an infrastructure failure | `no-match` |
| `claimed-in-event` | The suite's e2e App, with `"role":"review"` added to the request body, which the mint must ignore | `no-match` |
| `forced-failure` | The registered review bot, sent with a credential that keeps the verified token's header and payload but replaces its signature | `lookup-failed` |

The same run makes a **control** lookup of the registered review bot, which must answer 200 `recognized` with role `review`, from a token the probe verified, with the leased repository as the OIDC `repository` claim. Otherwise the row fails as an infrastructure failure. As in the mint probe, each job verifies its own OIDC token first (`oidc_verified`). Probe-side configuration, OIDC and transport failures are always infrastructure failures, and so is a lookup-mode answer that `botidentity.ParseMintBotRoleResponse` rejects. A forced failure counts as `lookup-failed` only for a verified token answered 401 `authentication failed`. Any other status is an infrastructure failure, and a 200 fails the row because the mint accepted an invalid credential. No infrastructure failure can satisfy an expected outcome.

The probe uploads only the request fields, `oidc_verified`, the two OIDC claims, the HTTP status, the `outcome`, `role` and `error` strings of the mint's body (each at most 300 characters), and a probe-side error class. It never writes a token. The remaining steps project the subject's outcome onto a normalized actor with `botidentity.ApplyBotRoleResult`, and check the event-level pre-CEL gate (`harnessdispatch.IsEventAuthorized`) for a non-label `review_submitted` event. A `no-match` and a `lookup-failed` are both denied before CEL, and `role_verified` (`true` and `false`) keeps them apart. `TestBotRoleLookupFeature_RowsMatchSteps` checks every Examples row against the step patterns and classification. Each of the 4 rows leases a pool repository.

## Version pinning for `fullsend-ai/agents`

External behaviour runners import the shared libraries from this module:

```go
require github.com/fullsend-ai/fullsend v0.x.y // released tag, not @main
```

Do not import `internal/mintcore` (or `internal/mintcore/mintconsts`) from packages reachable from `pkg/behaviourtest`. The nested mintcore module is resolved only by a local `replace` that downstream modules do not inherit; a leak makes `go build github.com/fullsend-ai/fullsend/pkg/behaviourtest` fail with `unknown revision internal/mintcore/v0.0.0`. Duplicate constants locally and keep the graph clean — see [Go Code](../../contributing/go-code.md).

The supported entry point is `behaviourtest.RunSuite`. Driver selection, slot leasing, CLI build, concurrency, tags, and step registration are handled internally from the same environment variables as the in-repo suite (`BEHAVIOUR_SCM`, `BEHAVIOUR_CI`, `BEHAVIOUR_INSTALL_MODE`, `ENVIRONMENT`, `BEHAVIOUR_CAPABILITIES`, `BEHAVIOUR_CONFIG_PRESET`, `GODOG_TAGS`, `GODOG_CONCURRENCY`):

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
driver, err := install.NewRepoPoolCFMintPreviews(install.PoolConfig{
    Layout:       install.PoolLayoutForEnvironment("dev", t.Logf),
    TokenFor:     tokenForOrg, // func(ctx, org) (string, error)
    NewClient:    func(token string) forge.Client { return newClient(token) },
    Binary:       binary,
    GCPProjectID: gcpProjectID,
    Logf:         t.Logf,
})
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

**Concrete drivers renamed:** `cfmint` → `RepoPoolCFMintPreviews`, `legacy` / `externalmint` → `RepoPoolExternalMint`. Drivers are named for the environments they manage. Concrete implementations live in the `install` package. `install.Factory` originally took `(org string, client forge.Client, token, binary, gcpProjectID string, logf func(string, ...any))` (see the cross-org slot leasing bullet below for the current signature); driver-specific config (PEMs, pool size, mint URL) is read from env or computed internally. `install.State`, `install.MintURLProvider`, `install.RepoEnsurer`, and `install.CFMintConfig` are removed from the exported surface. External code should only reference `install.Factory` and `install.Driver`.

**Cross-org slot leasing ([#8622](https://github.com/fullsend-ai/fullsend/issues/8622)):** `install.Factory` now takes an `install.PoolConfig` (pool layout, per-org `TokenFor`, `NewClient`, CLI binary, GCP project, logger) instead of one org's client and token. The returned driver leases `(org, repo)` slots across the pool orgs and implements the new `install.LeaseDriver` (`AllocateLease`, `DeallocateLease`, returning an `install.RepoLease` with the org's token, client and mint URL); its name-only `AllocateRepo`/`DeallocateRepo` return an error. `install.PlaybackDriver` forwards the lease methods. A lease driver also implements `install.LeaseVerifier` (`StillOwnsLease`) so scenario cleanup can fail closed when another holder has replaced the slot. `World` gains `LeasedRepoOrg`, `MintURL`, `OrgDrivers` and `LeaseOwned`, and the enrolled-repository step rebinds `Org`, `Token`, SCM and CI to the leased org. `e2etest.TokenForOrg` replaces `e2etest.AcquireOrg` in the suite runners, which no longer take the exclusive `e2e-lock` or run `e2etest.CleanupStaleResources`; admin e2e tests still use both.

**Shared public preview mints ([#8681](https://github.com/fullsend-ai/fullsend/issues/8681)):** the DEV driver deploys one public preview mint per mint group instead of one per org, and every mint uses one job-wide status group. `install.RepoLease` gains `MintStatusGroup` and `MintWorkflowHostRepos`, and `World` gains the matching `MintStatusGroup` and `MintWorkflowHostRepos`, set by the enrolled-repository step. See [Repo allocation via the shared slot driver](#repo-allocation-via-the-shared-slot-driver).

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

**`scm.Driver.CreateUserFork` addition (breaking change):** The `scm.Driver` interface now includes `CreateUserFork(ctx, owner, repo string) (forkOwner, forkRepo string, err error)`, which forks a repository into the authenticated user's own account, the way an outside contributor does. The forge picks the fork's name. The `owned by the <actor>` fork step uses it. The GitHub and GitLab reference implementations pass through to the existing `forge.Client.CreateFork` method. This widens the required method set, so external `scm.Driver` implementations must add this method when upgrading past this release or they will no longer satisfy the interface.

**`scm.Driver.ListOpenIssues` addition (breaking change):** The `scm.Driver` interface now includes `ListOpenIssues(ctx, owner, repo string, labels ...string) ([]forge.Issue, error)`, returning the repository's open issues (pull requests excluded) that carry every given label. The `an issue with label ... and title containing ... exists` step uses it. The GitHub and GitLab reference implementations pass through to the existing `forge.Client` method of the same name. This widens the required method set, so external `scm.Driver` implementations must add this method when upgrading past this release or they will no longer satisfy the interface.

**`ci.RunLister` optional interface:** `ListRecentRuns(ctx, owner, repo string, limit int) ([]forge.WorkflowRun, error)` lists a repository's most recent workflow runs across all workflows. Failure log collection uses it to find runs that no step resolved. It is a separate interface, not a `ci.Driver` method, so external `ci.Driver` implementations keep compiling. Without it, failed scenarios still get a failure summary but only the run a step resolved has its logs collected. The GitHub Actions and GitLab CI reference drivers implement it.

**`world.World.MintProbe` addition:** The new `MintProbe *world.MintProbe` field (with the `world.MintProbe` and `world.MintProbeResult` types) holds the state of the [GitHub mint scope scenarios](#github-mint-scope-scenarios). It is a non-breaking addition. The mint probe steps need a CI driver that implements the optional `ci.RunLister` interface, as the GitHub Actions driver does.

**`install.MintProbeHostRepoSuffix` addition:** The suffix (`-mint-host`) of each pool slot's mint-probe host repository. The CF mint drivers add those names to the mint's `--workflow-host-repos`.

**`install.MintURLReporter` optional interface and `install.MintStatusTeamSlug`:** `MintURL() string` reports the suite's mint URL. The [CLI mint status scenarios](#cli-mint-status-scenarios) use it to get the mint address. It is a separate interface, not an `install.Driver` method, so external `install.Driver` implementations keep compiling. The mint status steps fail with a `deployment` error on a driver without it. `MintStatusTeamSlug` (`mint-status`) names the team the CF mint drivers pass as `--status-github-group <org>/mint-status`.

**`world.World.CLIBinary`, `world.World.WriteToken` and `world.World.MintStatus` additions:** `RunSuite` sets `CLIBinary` to the CLI it builds and `WriteToken` to `TEST_ACTOR_WRITE_PAT`. `MintStatus` (type `world.MintStatus`) holds the CLI mint status scenario state. All three are non-breaking additions. A custom bootstrap must set `CLIBinary` and `WriteToken` to run the mint status scenarios.

**`world.World.BotRoleProbe` addition:** The new `BotRoleProbe *world.BotRoleProbe` field (with the `world.BotRoleProbe`, `world.BotRoleProbeResult` and `world.BotRoleProbeResponse` types) holds the state of the [GitHub bot-role lookup scenarios](#github-bot-role-lookup-scenarios). It is a non-breaking addition. Like the mint probe steps, the bot-role probe steps need a CI driver that implements `ci.RunLister`.

**`install.AppSet` addition:** `AppSet()` returns the GitHub App set the behaviour mints are deployed with (`BEHAVIOUR_APP_SET`, default `fullsend-test`). The bot-role lookup steps derive registered bot logins (`<app set>-<role>[bot]`) from it.

**`ci.EmptyMatrixWaiter` optional interface:** `WaitForHarnessEmptyMatrix(ctx, owner, repo string, after time.Time) (*forge.WorkflowRun, error)` backs the `the harness dispatch completes with an empty matrix` step. It is a separate interface, not a `ci.Driver` method, so external `ci.Driver` implementations keep compiling; the step fails with an explanatory error on a driver without it. Only the GitHub Actions reference driver implements it.

Bump the pinned version when behaviour step vocabulary or `pkg/behaviourtest` APIs change.
