# Behaviour test drivers

Behaviour tests isolate forge-specific code behind drivers so Gherkin scenarios stay portable.

## Interfaces

| Interface | Package | Responsibility |
|-----------|---------|----------------|
| `scm.Driver` | `pkg/behaviourtest/drivers/scm` | Issues, comments, labels (via GetIssue), file commits |
| `ci.Driver` | `pkg/behaviourtest/drivers/ci` | Workflow polling, logs, artifact download |
| `install.Driver` | `pkg/behaviourtest/drivers/install` | Unified surface: repo allocation/deallocation, mint lifecycle, and suite teardown |
| `install.Factory` | `pkg/behaviourtest/drivers/install` | `func(install.PoolConfig) (Driver, error)`: builds the one shared `Driver` over the pool orgs; `PoolConfig` carries the pool layout, per-org `TokenFor` and `NewClient`, CLI binary, GCP project, and logger |
| `install.LeaseDriver` | `pkg/behaviourtest/drivers/install` | A `Driver` that leases `(org, repo)` slots across the pool orgs: `AllocateLease` returns an `install.RepoLease` (org, repo, token, client, mint URL) and `DeallocateLease` releases it. The shared pool driver's name-only `AllocateRepo`/`DeallocateRepo` return an error |
| `install.LeaseVerifier` | `pkg/behaviourtest/drivers/install` | Optional companion to `LeaseDriver`: `StillOwnsLease(ctx, org, repo)` reports whether the repo under a leased slot name is still this lease's. Scenario cleanup calls it before each name-based mutation and skips the mutation when the answer is no or cannot be determined, so a `LeaseDriver` that does not implement it has all cleanup mutations skipped |

v1 reference implementations:

- `pkg/behaviourtest/drivers/scm/github/`
- `pkg/behaviourtest/drivers/scm/gitlab/`
- `pkg/behaviourtest/drivers/ci/githubactions/`
- `pkg/behaviourtest/drivers/ci/gitlabci/`
- `pkg/behaviourtest/drivers/install/repopool_cfmint_previews.go` (RepoPoolCFMintPreviews)
- `pkg/behaviourtest/drivers/install/repopool_cfmint_stage.go` (RepoPoolCFMintStage)
- `pkg/behaviourtest/drivers/install/repopool_external_mint.go` (RepoPoolExternalMint)
- `pkg/behaviourtest/drivers/install/common/setup.go` (shared helpers: `RunGitHubSetup`, `ResolveInferenceWIFProvider`, `InferenceStatusWIFProvider`, `ProvisionInference`)

## Runner configuration

Set when starting the suite (not in feature files):

```
BEHAVIOUR_SCM=github              # also: gitlab; future: forgejo
BEHAVIOUR_CI=githubactions        # also: gitlabci; future: tekton
BEHAVIOUR_INSTALL_MODE=per-repo   # v1 default and only supported value
BEHAVIOUR_CONFIG_PRESET=          # optional local path or HTTPS URL forwarded as github setup --config
PLAYBACK_RUNTIME=                 # unset: normal "dummy" runtime; "dummy-playback": installs with playback tracking hooks (see behaviour-testing.md)
ENVIRONMENT=dev                   # mint/infra target: dev (default) or stage
```

The suite in `e2e/behaviour/suite_test.go` (or an external runner) calls `behaviourtest.RunSuite`, which selects an `install.Factory` from `ENVIRONMENT` (e.g. `install.NewRepoPoolCFMintPreviews(pool)`) to get the shared `install.Driver` that leases `(org, repo)` slots across the pool orgs and owns mint deploy, repo install, and teardown. It does not reserve an org (`e2etest.AcquireOrg`) or run broad startup cleanup. It runs godog with `pkg/behaviourtest/suite.InitScenario`, which clones a template `*world.World` per scenario. When a scenario calls "Given the enrolled test repository", `install.LeaseDriver.AllocateLease` claims a free slot, creating its repo. A slot whose repo already exists belongs to another holder and is never reset or deleted. The driver then installs fullsend with that org's credential and mint, and the step rebinds the World's org, token, mint URL, and SCM/CI drivers (built from `BEHAVIOUR_SCM` / `BEHAVIOUR_CI` via `World.OrgDrivers`) to the leased org. `DeallocateLease` deletes the slot repo, if it is still this lease's, and frees the slot in the After hook, after scenario cleanup and debug collection. `Driver.Finalize` releases outstanding leases with an error and tears down each deployed mint once. See [Repo allocation via the shared slot driver](behaviour-testing.md#repo-allocation-via-the-shared-slot-driver). Unsupported `BEHAVIOUR_INSTALL_MODE` or `ENVIRONMENT` values fail at suite startup. `ENVIRONMENT` is `dev` or `stage` (empty defaults to `dev`).

### Install driver (unified)

The suite uses a single unified `install.Driver` constructed via `install.Factory`. The suite selects the factory based on `ENVIRONMENT`:

| Environment | Factory | Mint | Org | Install mode |
|-------------|---------|------|-----|-------------|
| `dev` (default) | `NewRepoPoolCFMintPreviews` | One public CF Worker preview per mint group leased in the run (two at 12 slots per org) | Pool orgs `halfsend-01` … `halfsend-12` | Vendored binary |
| `stage` | `NewRepoPoolCFMintStage` | Durable CF Worker at `stage-mint.fullsend.sh` | `halfsend` (single unsuffixed org) | Non-vendored, `--fullsend-ref=main` |

Each factory returns the same shared slot driver (`composed.go`), which owns the full lifecycle:

1. Leases `(org, test-repo-NN)` slots across the pool orgs through the slot allocator (`slotpool.go`). A claim creates the slot repo, and an existing repo is another holder's and is never touched. The allocator also holds each org's shared `e2e-lock` and runs the repo-slot GC.
2. On the first lease in any org of a mint group, deploys the group's mint (RepoPoolCFMintPreviews: public CF Worker preview; RepoPoolCFMintStage: durable Worker with custom domain; RepoPoolExternalMint: pre-configured URL, one group of every org). The orgs of the group share that deployment, its URL and its teardown. On each org's first lease the driver also creates that org's ensurer with the org's installation token and its group's mint URL.
3. Installs fullsend into each claimed repo (concurrent-safe via singleflight). The ensurer never creates, deletes or resets the slot repo. The allocator's release deletes the repo, checking it is still the lease's, so the next lessee cannot inherit leftover state.
4. Implements `install.LeaseDriver` (`AllocateLease` / `DeallocateLease`) and `install.LeaseVerifier` (`StillOwnsLease`, which scenario cleanup needs to mutate anything) plus `Finalize` / `Capacity`. The name-only `AllocateRepo` / `DeallocateRepo` return an error.

The Factory takes an `install.PoolConfig`: the pool layout, a per-org token source (`e2etest.TokenForOrg` in the runners), a client constructor, the CLI binary, the GCP project, and the logger. Driver-specific inputs (PEMs, allowlists, mint URL, optional `BEHAVIOUR_CONFIG_PRESET`) come from env or are computed per org inside the driver. When `BEHAVIOUR_CONFIG_PRESET` is set, `github setup` receives `--config <value>` and omits `--runtime dummy` so the preset's `runtime: dummy` is inherited rather than pinned in the overlay. The suite does not construct or thread pool, ensurer, or mint driver types directly — all internal lifecycle is encapsulated inside the concrete driver returned by the factory. Godog concurrency is controlled independently of slot capacity. It defaults to the slots per org (12), not `driver.Capacity()` (every slot of every pool org), and `GODOG_CONCURRENCY` overrides it (warn, do not fail, if concurrency > Capacity).

Pool orgs must already have the six `fullsend-test-*` role apps and the production `fullsend-ai-e2e` app installed, plus per-repo mint enrollment for each numbered repo (one-time GCP admin step on the hosted mint project). The driver does not run `fullsend admin install` or `fullsend mint enroll`. See [e2e-testing.md](e2e-testing.md#behaviour-tests-and-per-repo-mint-enrollment).

The two CF mint drivers compute mint groups from the pool layout on every run (`planMintGroups` in `mint_groups.go`); there is no hardcoded org-to-mint list. A group's `--workflow-host-repos` lists its orgs' pool repos plus each slot's mint-probe host `<org>/test-repo-NN-mint-host` (`install.MintProbeHostRepoSuffix`). The driver packs orgs in order into the fewest groups whose actual serialized list stays within 5,000 bytes, a margin under Cloudflare's 5 KB per-variable limit. At 12 slots per org a list for six orgs is 4,319 bytes and for seven is 5,039 bytes, so the 12 DEV orgs use two mints. A changed slot or org count regroups automatically. If one org's list alone exceeds the limit, the factory fails and names the org (one org's slots are not split across mints). The [GitHub mint scope scenarios](behaviour-testing.md#github-mint-scope-scenarios) create and delete the probe hosts per scenario. Because the lists are deploy flags, a change takes effect on the next suite run's mint deploy, with no GCP enrollment step.

RepoPoolCFMintPreviews deploys its previews with `--public` (`PER_REPO_WIF_REPOS=*`) instead of `--per-repo-wif-repos`, so no caller list has to fit in a Worker variable or be kept enrolled. RepoPoolCFMintStage keeps `--per-repo-wif-repos` for its durable mint. See [Public preview mints](behaviour-testing.md#public-preview-mints) for the accepted trade-off.

Both CF mint drivers also deploy with `--status-auth oidc,github --status-github-group <org>/mint-status` (`install.MintStatusTeamSlug`), so members of that `mint-status` team can query the mint's status with a GitHub user token (see [Mint status team](e2e-testing.md#mint-status-team)). The driver picks `<org>` at random from the pool orgs once per job and uses it for every mint, so it need not be an org the mint serves. The shared driver reports each lease's mint URL, status group and workflow hosts in `install.RepoLease` (`MintURL`, `MintStatusGroup`, `MintWorkflowHostRepos`), which the lease step stores in the matching `World` fields. The [CLI mint status scenarios](behaviour-testing.md#cli-mint-status-scenarios) lease a slot to get it, and fall back to the optional `install.MintURLReporter` interface for other drivers.

When the [job-scoped trace collector](behaviour-testing.md#job-scoped-trace-collection) runs, RepoPoolCFMintPreviews starts it once, before any mint deploy, and adds `--otlp-traces-endpoint` and `--otlp-headers-file` to every group's deploy, so each preview mint exports its spans to the same collector as the workflows. Leased repos are pointed at the collector with their org's client. RepoPoolCFMintStage does not trace its mint, because its durable mint is shared across runs.

`Finalize` (RepoPoolCFMintPreviews) abandons every org's preview alias via `fullsend mint delete --platform=cloudflare` and reclaims any outstanding leases with an error. The RepoPoolCFMintStage driver's teardown is a no-op (the durable Worker persists across runs). The RepoPoolExternalMint driver's teardown is a no-op.

## Adding an SCM driver

1. Implement `scm.Driver` in `pkg/behaviourtest/drivers/scm/<vendor>/`.
   Also implement the optional `scm.PullRequestHeadSHAReader` interface
   (`GetPullRequestHeadSHA`). Dummy-agent fixtures that use the
   `<pr_head_sha>` placeholder need it.
2. Register the driver in `behaviourtest.RunSuite` when `BEHAVIOUR_SCM=<vendor>`.
3. Document the env var value here.
4. Add `@skip:<vendor>` tags on scenarios that cannot run until the driver is complete.

Use `forge.Client` for operations it already exposes; add REST helpers inside the driver package only when necessary (e.g. `GetIssue` with labels).

## Adding a CI driver

1. Implement `ci.Driver` — `WaitForWorkflow`, `FindCompletedWorkflowRun`, `AssertNoWorkflow`, `GetRunLogs`, `DownloadArtifacts`, `DownloadNamedArtifactFromRun`, `DownloadNamedArtifactAfter`, `WaitForHarnessAgent`, `WaitForHarnessAgentRound`, `WaitForFailedHarnessAgent`, `AssertNoHarnessAgentArtifact`, `CountHarnessDispatches`.
2. Also implement the optional `ci.RunLister` interface (`ListRecentRuns`). A failed scenario's After hook uses it to save the logs of runs that no step resolved before the leased repository is deleted. Without it, only the run a step resolved has its logs collected.
3. Also implement the optional `ci.EmptyMatrixWaiter` interface (`WaitForHarnessEmptyMatrix`). The `the harness dispatch completes with an empty matrix` step needs it to assert that a harness run succeeded without dispatching any agent.
4. Map forge `WorkflowRun` types to portable polling logic; reuse patterns from the GitHub Actions driver in `pkg/behaviourtest/drivers/ci/githubactions/`.
5. Register in `behaviourtest.RunSuite` for the matching `BEHAVIOUR_CI` value.

## Adding an install driver

1. **Discover existing env vars.** Read `.github/workflows/e2e.yml` for
   secrets and env vars already wired into the BT job (search for `env:`
   blocks in the behaviour test step). Use existing vars — e.g.,
   `TEST_*_PEM` for role PEMs, `TEST_CLOUDFLARE_*` for CF credentials —
   rather than inventing new ones. Cross-reference
   [e2e-testing.md](e2e-testing.md#test-github-apps) for the full
   secrets inventory and app-to-PEM mapping.
2. **Discover CLI flag surface.** Read the CLI source for the commands
   your driver will invoke (e.g., `internal/cli/mint.go` for
   `mint deploy`, `internal/cli/mint_delete.go` for `mint delete`).
   Check the full flag surface — especially optional flags like
   `--worker-name`, `--workflow-host-repos`,
   `--per-repo-wif-repos`, `--app-set`, and `--pem-dir`. Ensure deploy
   and teardown commands receive symmetric identifying flags (e.g., both
   `mint deploy` and `mint delete` need `--worker-name` if the Worker
   name is non-default).
3. **Handle app set.** Test PEMs belong to the `fullsend-test` app set,
   not the default `fullsend-ai`. Pass `--app-set fullsend-test`
   explicitly when deploying with test PEMs. Omitting this causes the
   CLI to validate PEMs against the wrong GitHub Apps.
4. **Implement `install.Driver`** in a new file under
   `pkg/behaviourtest/drivers/install/`. Each driver variant lives in
   the same package (e.g., `repopool_cfmint_previews.go`,
   `repopool_external_mint.go`) behind the shared `install.Driver`
   interface. Place common helpers shared across drivers in
   `install/common/` (e.g., `RunGitHubSetup`, `ResolveInferenceWIFProvider`).
5. **Register the driver** in `behaviourtest.RunSuite` (`installFactoryFor`
   in `pkg/behaviourtest/select.go`). Install driver selection is keyed by
   `ENVIRONMENT`, not `BEHAVIOUR_INSTALL_MODE`.
6. **Use `repopool_external_mint.go`** (~71 lines) as the minimal
   reference implementation. For a more complex example showing CLI arg
   construction, preview alias generation, and teardown semantics, see
   `repopool_cfmint_previews.go`.
7. **Export CLI arg builders.** Export functions like `DeployArgs` and
   `TeardownArgs` so unit tests can verify arg construction without
   shelling out to the real CLI.
8. **Document the new driver** here — add the env var value and any new
   secrets or configuration required.

## Step definitions

Steps must **not** import forge-specific packages (`internal/forge/github`, `internal/forge/gitlab`) directly — only drivers. This keeps scenarios vendor-agnostic.

Steps use `w.Org` and `w.RepoName` (the allocated repo name) plus per-repo constants from the `install` package (`PerRepoTriageWorkflow`, `PerRepoAgentWorkflow`, `PerRepoAgentArtifact`) for workflow and artifact paths.

## Testing drivers

Prefer unit tests with `httptest` for REST helpers. Optional smoke scenarios against live backends use the behaviour suite's credentials (`GITHUB_TOKEN`, halfsend org pool).

## Future backends checklist

- [x] GitLab SCM driver (implemented; `@skip:gitlab` tag removal pending)
- [x] GitLab CI driver (implemented; suite wiring currently uses a GitHub-backed `forge.Client` — not yet live-testable against real GitLab backends)
- [ ] Tekton CI driver
- [ ] Non-GitHub install backends
