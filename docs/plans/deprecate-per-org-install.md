# Implementation Plan: Deprecate Per-Org Installation Mode (ADR-0044)

## Conventions

Line numbers in this plan are pinned to the codebase at the time of
writing and will drift as PRs merge. Function and symbol names are
the primary references; line numbers are supplementary aids for initial
orientation. When implementing a PR, use the function/symbol name to
locate the current position rather than relying on line numbers.

## Context

ADR-0044 deprecates the per-org installation mode in favor of per-repo
([ADR 0033](../ADRs/0033-per-repo-installation-mode.md)) as the sole
supported model. The work proceeds in two phases: Phase 1 adds deprecation
warnings and a migration command (shipping in v1.x); Phase 2 removes all
per-org code (shipping as v2.0 breaking change).

[ADR 0045](../ADRs/0045-forge-portable-harness-schema.md) (forge-portable
harness schema) reinforces this deprecation: agent identity has moved from
`config.yaml`'s centralized `agents:` block into individual harness files,
and harness composition via `base` URLs replaces the `.fullsend` config
repo as the mechanism for sharing org defaults. ADR 0045's implementation
(Phases 1–4) is complete — the `.fullsend` config repo no longer serves as
the agent definition authority or the customization distribution point;
both roles are handled by harness files with `base` references.

The per-org model touches these subsystems:

| Subsystem | Key files | Org-specific surface |
|-----------|-----------|---------------------|
| CLI commands | `internal/cli/admin.go`, `internal/cli/github.go` | `runInstall`, `runUninstall`, `runEnableRepos`, `runDisableRepos`, `runDryRun`, `runAnalyze`, per-org flags |
| Layer stack | `internal/layers/configrepo.go`, `dispatch.go`, `enrollment.go`, `secrets.go`, `inference.go`, `vendorbinary.go` | `ConfigRepoLayer`, `DispatchTokenLayer`, `EnrollmentLayer`, `buildLayerStack()` |
| Config | `internal/config/config.go` | `OrgConfig`, `NewOrgConfig`, `ParseOrgConfig`, `EnabledRepos`, `DisabledRepos`, `DefaultAgentRoles` |
| Scaffold | `internal/scaffold/fullsend-repo/` | `dispatch.yml`, thin callers, `shim-workflow-call.yaml`, `repo-maintenance.yml`, `CustomizedDirs()` |
| Forge interface | `internal/forge/forge.go`, `github/github.go`, `fake.go` | 11 org-level methods to remove (5 secret, 5 variable, `ListOrgRepos`), 2 retained (`ListOrgInstallations`, `GetOrgPlan`), `PerRepoGuardVar` |
| Dispatch | `internal/dispatch/dispatch.go`, `gcf/provisioner.go` | `OrgSecretNames`, `OrgVariableNames`, PEM-sharing logic, org-wide WIF |
| Appsetup | `internal/appsetup/appsetup.go` | `findExistingInstallation` (org-level app discovery) |
| Mint | `internal/mintcore/claims.go` | `.fullsend` workflow ref pattern, `ValidateOrgAllowed` |
| Reusable workflows | `.github/workflows/reusable-*.yml` | `install_mode` input branching |
| Actions | `.github/actions/validate-enrollment/action.yml` | per-org enrollment validation |
| Web admin | `web/admin/src/lib/layers/`, `web/admin/src/lib/orgs/` | `configRepo.ts`, `enrollment.ts`, `dispatch.ts`, `orgConfigParse.ts`, `constants.ts` (`CONFIG_REPO_NAME`); `orgListRow.ts`, `installReadinessProbes.ts` (config-repo checks) |
| E2E tests | `e2e/admin/` | `TestAdminInstallUninstall`, org pool locking, org cleanup |

## PR Dependency Graph

```
Phase 1 (deprecation, v1.x):

PR 1 (deprecation warnings) ──┐
                               ├──> PR 4 (migration command) ──> PR 5 (migration e2e test)
PR 2 (docs update) ───────────┘
PR 3 (per-repo e2e test)  [independent, no deps]

Phase 2 (removal, v2.0):

PR 5 ──> PR 6 (remove CLI) ──┬──> PR 10 (remove forge methods) ──> PR 12 (simplify appsetup)
                              ├──> PR 11 (simplify dispatch)
                              ├──> PR 13 (update mint)
                              └──┬─> PR 14 (update workflows)
                                 │
         PR 7 (remove layers)  ──┤
         PR 8 (remove config)  ──┤──> PR 15 (remove e2e, final cleanup)
         PR 9 (remove scaffold) ─┘
```

PRs 6–9 can be developed in parallel after Phase 1 merges (the graph shows
code-level dependencies; all Phase 2 PRs also depend on Phase 1 completing
as a process prerequisite). PRs 10–11, 13–14 depend on PR 6 (CLI callers
removed). PR 12 depends on PR 10 (forge interface updated). PR 15 is the
final sweep after all prior PRs merge.

---

## Phase 1: Deprecation

### PR 1: Add deprecation warnings to per-org CLI paths

**Scope:** Emit deprecation warnings. Zero behavioral change.

**`internal/cli/admin.go`:**

Add a helper at the top of the file:

```go
func warnPerOrgDeprecated(p printer.Printer) {
	p.Warnf("Per-org installation is deprecated and will be removed in v2.0.")
	p.Warnf("Migrate to per-repo: fullsend admin install <owner/repo>")
	p.Warnf("See https://docs.fullsend.dev/migration/per-org-to-per-repo")
}
```

Call `warnPerOrgDeprecated(p)` at the start of:

- `runInstall()` (~line 1498) — per-org install
- `runUninstall()` (~line 1615) — per-org uninstall
- `runDryRun()` (~line 1163) — per-org dry-run
- `runAnalyze()` (~line 1773) — per-org analyze
- `runEnableRepos()` (~line 2155) — enable repos
- `runDisableRepos()` (~line 2335) — disable repos

**`internal/cli/github.go`:**

Call `warnPerOrgDeprecated(p)` at the start of:

- `runGitHubSetupPerOrg()` (~line 310) — github setup for org
- `newGitHubEnrollCmd()` / `newGitHubUnenrollCmd()` — github enroll/unenroll
  (these delegate to `runEnableRepos()` / `runDisableRepos()` in admin.go)

**Tests:**

- Add test cases in `internal/cli/admin_test.go` verifying deprecation
  warning appears in output for per-org `install`, `uninstall`,
  `enable repos`, `disable repos`.
- Add test case in `internal/cli/github_test.go` verifying warning for
  `github setup <org>`.

**After merge:** All per-org commands emit deprecation warnings. No
behavioral change.

---

### PR 2: Update documentation

**Scope:** Documentation-only. No code changes.

**`docs/guides/`:**

- Update admin install guide: present per-repo as primary, per-org as
  deprecated with a callout box.
- Add migration guide page at `docs/guides/admin/migrate-per-org-to-per-repo.md`
  with the step-by-step migration procedure from ADR-0044 section 3.
  Include a section on using ADR 0045's `base` harness composition to
  replicate org-wide defaults without a centralized config repo.

**`docs/architecture.md`:** (deferred from ADR-only PR to this PR so
architecture updates ship alongside the deprecation warnings, not before
users can see them)

- Add deprecation notice to per-org architecture sections.
- Update installation overview to lead with per-repo.

**`README.md`:**

- Update installation quick-start to use per-repo example.
- Add deprecation notice for per-org references.

**After merge:** Documentation reflects per-repo as primary. Migration
guide published.

---

### PR 3: Add per-repo e2e test

**Scope:** New test coverage. No changes to existing tests.

**Create `e2e/admin/per_repo_test.go`:**

Test the per-repo lifecycle:

1. **Install:** `fullsend admin install <owner>/<repo>` with
   `--skip-app-setup`, `--mint-url`, `--inference-project`.
2. **Verify scaffold:** Check `.github/workflows/fullsend.yml` exists on
   default branch, `.fullsend/config.yaml` exists, repo variables
   `FULLSEND_MINT_URL` and `FULLSEND_PER_REPO_INSTALL` are set.
3. **Triage smoke test:** Create issue, wait for triage agent comment.
4. **Cleanup:** Delete workflow file, remove repo variables/secrets.

Use existing `testutil.go` helpers for PAT creation and GitHub auth.
Use repo-level cleanup (no org-level cleanup needed).

**After merge:** Per-repo installation path has e2e coverage.

---

### PR 4: Add `fullsend admin migrate` command

**Scope:** New subcommand. No changes to existing commands.

**Depends on:** PR 1 (deprecation warnings wired up), PR 2 (migration
guide exists to link to).

**`internal/cli/admin.go`:**

Add `newMigrateCmd()` under `newAdminCmd()`:

```go
func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate <org> [repo...]",
		Short: "Migrate repos from per-org to per-repo installation",
	}
	// flags: --all, --dry-run, --mint-url, --inference-project,
	//        --inference-region, --skip-app-setup, --skip-mint-deploy
	return cmd
}
```

**`internal/cli/migrate.go`** (new file):

Implement `runMigrate()`:

1. Load existing org config from `.fullsend/config.yaml` via
   `forge.GetFileContent()` + `config.ParseOrgConfig()`.
2. Determine target repos: if `--all`, use `OrgConfig.EnabledRepos()`;
   otherwise use positional args.
3. For each repo (sequentially — per-repo WIF registration is not
   concurrent-safe):
   a. Check if `FULLSEND_PER_REPO_INSTALL` already set (skip if so).
   b. Run the per-repo install flow (reuse `runPerRepoInstall()` logic
      with appropriate flags).
   c. Copy customizations: read `customized/` from `.fullsend` config
      repo, commit to `<repo>/.fullsend/customized/` via
      `forge.CommitFiles()`. For orgs using ADR 0045 harness
      composition, harness files with `base:` URLs referencing shared
      upstream harnesses replace per-org `customized/` overrides —
      the migration command should generate thin harness wrappers
      with `base:` when harness files exist in the source config.
   d. Verify: check repo variable `FULLSEND_PER_REPO_INSTALL` is set
      and workflow file exists.
   e. Disable per-org enrollment: call `runDisableRepos()` for this
      repo (updates `config.yaml`, triggers `repo-maintenance.yml`).
   f. Report: print status (success/failure/skipped) for each repo.
4. If `--dry-run`: report what would happen without making changes.

**`internal/cli/migrate_test.go`** (new file):

- Test `--dry-run` output.
- Test single-repo migration with fake forge client.
- Test `--all` flag reads from org config.
- Test skip behavior when guard variable already set.
- Test error handling: repo not found, per-repo install fails.

**After merge:** `fullsend admin migrate <org> [repo...] [--all]` is
available. Existing commands unchanged.

---

### PR 5: Migration e2e test

**Scope:** E2e test for the migration flow. Depends on PR 4.

**Create `e2e/admin/migrate_test.go`:**

1. **Setup:** Run per-org install on test org (reuse existing
   `TestAdminInstallUninstall` setup logic).
2. **Migrate:** Run `fullsend admin migrate <org> <test-repo>`.
3. **Verify per-repo:** Confirm per-repo shim exists, repo variables
   set, triage agent responds to a test issue.
4. **Verify per-org removed:** Confirm repo is no longer in
   `config.yaml` enrollment.
5. **Cleanup:** Delete per-repo artifacts, uninstall per-org.

**After merge:** Migration path is e2e-tested. Phase 1 is complete.

---

## Phase 2: Removal

### PR 6: Remove per-org CLI commands and flags

**Scope:** Remove per-org entry points from the CLI. Breaking change.

**`internal/cli/admin.go`:**

- Remove `runInstall()` (~lines 1498–1612) — the per-org install
  orchestrator.
- Remove `runUninstall()` (~lines 1615–1770) and `newUninstallCmd()`
  (~lines 1070–1126).
- Remove `runDryRun()` (~lines 1163–1232) per-org path.
- Remove `runAnalyze()` (~lines 1773–1817) per-org path.
- Remove `newEnableCmd()`, `newEnableReposCmd()`, `runEnableRepos()`
  (~lines 2055–2286).
- Remove `newDisableCmd()`, `newDisableReposCmd()`, `runDisableRepos()`
  (~lines 2334–2449).
- Remove `newMigrateCmd()` and `runMigrate()` (migration tool no longer
  needed after removal).
- Remove `--enroll-all`, `--enroll-none` `BoolVar` definitions (~lines
  551–552), `perOrgOnlyFlags` list (~lines 114–116), and validation
  (~lines 281–285).
- Remove `warnPerOrgDeprecated()` helper (from PR 1).
- Remove per-org helpers: `loadExistingInferenceProvider()`,
  `loadExistingEnabledRepos()`, `loadKnownSlugs()`,
  `collectEnrolledRepoIDs()`, `syncOrgVariableVisibility()`.
- Simplify `newInstallCmd()` argument parsing: require `owner/repo`
  format (reject bare org names).
- Remove `buildLayerStack()` — per-repo does not use the layer stack
  (it sets variables/secrets directly).

**`internal/cli/github.go`:**

- Remove per-org path (`runGitHubSetupPerOrg()` and its internal
  helpers: `buildLayerStack` callers ~lines 454/490,
  `ensureConfigRepoExists` ~line 472).
- Remove `newGitHubEnrollCmd()`, `newGitHubUnenrollCmd()` and their
  delegated run functions (`runEnableRepos()`, `runDisableRepos()` are
  removed in PR 6's admin.go changes above).
- Simplify `parseTarget()` to remove org-mode detection branching
  (the function is shared with per-repo paths; only remove the
  org-only logic, not the function itself).

**`internal/cli/migrate.go`:**

- Delete entire file (migration command removed with per-org).

**Test updates:**

- `internal/cli/admin_test.go`: Remove per-org test cases, add test that
  bare org name is rejected.
- `internal/cli/github_test.go`: Remove per-org test cases, update
  remaining tests.
- `internal/cli/migrate_test.go`: Delete entire file.

**After merge:** CLI only accepts `fullsend admin install <owner/repo>`.
All per-org commands gone.

---

### PR 7: Remove per-org layers

**Scope:** Remove layer implementations that are per-org only.

**Delete files:**

- `internal/layers/configrepo.go` — `ConfigRepoLayer` (creates/manages
  `.fullsend` config repo).
- `internal/layers/enrollment.go` — `EnrollmentLayer` (dispatches
  `repo-maintenance.yml`, manages enrollment).
- `internal/layers/workflows.go` — writes exclusively to
  `forge.ConfigRepoName` (`.fullsend` config repo).
- `internal/layers/harnesswrappers.go` — references
  `forge.ConfigRepoName` for harness wrapper commits.
- Corresponding test files: `configrepo_test.go`, `enrollment_test.go`,
  `workflows_test.go`, `harnesswrappers_test.go`.

**Modify files:**

**`internal/layers/dispatch.go`:**

- Remove `DispatchTokenLayer` / `OIDCDispatchLayer` entirely (manages
  org-level `FULLSEND_MINT_URL` variable, org variable visibility).
- If any shared dispatch utility functions remain, keep them; otherwise
  delete the file.

**`internal/layers/secrets.go`:**

- Remove the config-repo storage path (`CreateRepoSecret` stores PEMs
  as repo-level secrets in the `.fullsend` config repo — not org-level
  secrets). If OIDC mode is a no-op (PEMs handled by mint provisioner),
  simplify or remove the layer entirely.

**`internal/layers/inference.go`:**

- Remove references to `.fullsend` config repo.
- Simplify to store inference secrets at the repo level only.

**`internal/layers/vendor.go`:**

- Remove `VendoredBinaryPath` constant (~line 17, `bin/fullsend` —
  per-org path).
- Rename `VendoredBinaryPathPerRepo` (~line 19) to
  `VendoredBinaryPath`.

**`internal/layers/vendorbinary.go`:**

- Simplify `binaryPath()` (~line 66) to always return
  `.fullsend/bin/fullsend` — remove the `ConfigRepoName` check
  (~line 67).
- Remove `isPerRepo()` check (~line 81) if no longer needed.

**`internal/cli/vendor.go`:**

- `prepareVendorFiles()` (~line 94): remove the
  `repo != forge.ConfigRepoName` check (~line 95) — always use
  per-repo paths.
- `vendorPathPrefix()` (~line 223): remove the
  `repo != forge.ConfigRepoName` branch (~line 224) — always return
  the per-repo prefix.
- `removeStaleVendoredAssets()` (~line 230): remove the `perRepo`
  parameter and its branching — always use per-repo asset paths.
- Update callers (~lines 66, 86, 201, 208) and
  `internal/cli/vendor_test.go` accordingly.

**`internal/layers/stack.go`** (or wherever `Stack` is defined):

- Keep `Stack`, `Layer` interface, `Preflight()`, `InstallAll()`,
  `UninstallAll()`, `AnalyzeAll()` if they are still used by per-repo.
- If per-repo does not use the layer stack (it uses direct API calls
  instead), remove the entire `layers` package.

**Test updates:**

- Delete `internal/layers/dispatch_test.go` (or remove org-specific
  test cases if shared utilities remain).
- Update `internal/layers/vendorbinary_test.go`: remove per-org path
  tests, rename per-repo tests.
- Update remaining layer tests to remove org references.

**After merge:** Layer stack contains only per-repo-relevant layers.

---

### PR 8: Remove per-org config structures

**Scope:** Simplify `internal/config/config.go`.

**`internal/config/config.go`:**

- Remove `OrgConfig` struct (~lines 44–106) and all its methods:
  `NewOrgConfig`, `ParseOrgConfig`, `EnabledRepos`, `DisabledRepos`,
  `AgentSlugs`, `DefaultRoles` (org-level).
- Remove `DefaultAgentRoles()` (~lines 95–100) which includes the
  `fullsend` dispatch role. Note: `NewPerRepoConfig()` (line 291)
  falls back to `DefaultAgentRoles()` — update this call to use the
  renamed `DefaultRoles()` (formerly `PerRepoDefaultRoles()`).
  **Behavioral change:** `DefaultAgentRoles()` returns `["fullsend",
  "triage", "coder", "review", "retro", "prioritize"]` while
  `PerRepoDefaultRoles()` returns `["triage", "coder", "review",
  "fix", "retro", "prioritize"]` (no dispatch role, adds fix role).
  After this rename, `NewConfig()` defaults will use per-repo roles —
  this is intentional: per-repo mode does not use the dispatch workflow.
  **Latent bug fix:** `NewPerRepoConfig()` currently falls back to
  `DefaultAgentRoles()` (the per-org set) when roles is nil — this is
  incorrect for per-repo mode. The rename fixes this. Add a release
  note that existing per-repo configs may contain the unused `fullsend`
  role (harmless but unnecessary).
  Note: `ValidRoles()` (~line 93) includes `fullsend` in its list.
  Retain it — existing per-repo configs may reference it, and removing
  it would break validation. The role becomes unused but still valid.
- Rename `PerRepoConfig` to `Config`.
- Rename `PerRepoDefaultRoles()` to `DefaultRoles()`.
- Rename `NewPerRepoConfig()` to `NewConfig()`.
- Rename `ParsePerRepoConfig()` to `ParseConfig()`.
- Remove `RepoConfig` struct and `Repos map[string]RepoConfig` (enrollment
  tracking).
- Remove `RepoDefaults` if it was only used in `OrgConfig`.
- Keep `DispatchConfig`, `InferenceConfig`, `AgentEntry` if used by
  `Config` (formerly `PerRepoConfig`).
- Migrate `AllowedRemoteResources` field (~line 80) from `OrgConfig`
  into the renamed `Config` struct and `ParseConfig()`. This field is
  accessed by `run.go` (~lines 189, 192, 201, 205, 237, 243) and
  `lock.go` (~lines 170, 173, 181, 185, 264, 268, 270) via
  `tryLoadFullsendConfig()`/`requireFullsendConfig()` (renamed from
  `tryLoadOrgConfig()`/`requireOrgConfig()`, which remain as var
  aliases) in `orgconfig.go`. Update `orgconfig.go` to use
  `config.ParseConfig()` instead of `config.ParseOrgConfig()` and
  rename functions/file accordingly (e.g. rename file to
  `configloader.go` or inline into callers).

**Update all callers:**

- `internal/cli/admin.go`: Update `config.DefaultAgentRoles()` call
  (line 551, `--agents` flag default in `newInstallCmd()`). Lines 1617,
  1624, and 1792 are inside `runUninstall()` and `runAnalyze()`, both
  deleted by PR 6. **Behavioral consequence:** the `--agents` flag
  default changes from `fullsend,triage,coder,review,retro,prioritize`
  to `triage,coder,review,fix,retro,prioritize`.
- `internal/cli/github.go`: Update `config.DefaultAgentRoles()` calls
  (lines 132, 837).
- `internal/cli/mint.go`: Update `config.DefaultAgentRoles()` call
  (line 44). **Behavioral consequence:** `defaultMintRoles()` will
  return the per-repo role set (no `fullsend` dispatch role, adds
  `fix`). The mint will no longer provision WIF for the dispatch role.
  Verify that orphaned `fullsend`-role WIF providers are cleaned up.
- `internal/scaffold/scaffold.go`: Update references.
- `internal/forge/github/types.go` has its own `DefaultAgentRoles()`
  (line 39) — this is a separate function, not a `config` import.
  Only referenced by `types_test.go:12`; remove or rename to
  `DefaultRoles()` for consistency. Effectively dead code after
  per-org removal.
- `internal/cli/orgconfig.go`: Update `config.ParseOrgConfig()` calls
  to `config.ParseConfig()`. The functions were already renamed to
  `tryLoadFullsendConfig()`/`requireFullsendConfig()` (PR #3000); var
  aliases `tryLoadOrgConfig`/`requireOrgConfig` can be removed.
  Rename file to `configloader.go` or inline.
- `internal/cli/run.go`: Update `tryLoadOrgConfig`/
  `requireOrgConfig` var alias calls (~lines 189, 198, 201, 235, 237) and
  `orgCfg.AllowedRemoteResources` accesses (~lines 192, 205, 243)
  to use the renamed config type and loader functions. Also update
  `config.ParseOrgConfig()` (~line 1974): this call reads
  `orgCfg.Defaults.StatusNotifications` — `PerRepoConfig` has no
  `Defaults` sub-struct. **Decision:** add
  `StatusNotifications *StatusNotificationConfig` directly to the
  renamed `Config` struct (flattened, not nested under `Defaults`)
  and update `ParseConfig()` to populate it. This keeps status
  notification configuration available in per-repo mode without
  importing the full `RepoDefaults` sub-struct.
- `internal/cli/lock.go`: Update `tryLoadOrgConfig`/
  `requireOrgConfig` var alias calls (~lines 170, 181, 264) and
  `orgCfg.AllowedRemoteResources` accesses (~lines 173, 185, 268,
  270) to use the renamed config type and loader functions.
- Any other files importing `config.PerRepoConfig` or
  `config.PerRepoDefaultRoles`.

**Test updates:**

- `internal/config/config_test.go`: Remove `OrgConfig` test cases,
  rename `PerRepoConfig` tests, update function names.
  `TestNewPerRepoConfig_DefaultRoles` (~line 565) asserts
  `DefaultAgentRoles()` as the expected fallback — update this to
  `DefaultRoles()` (formerly `PerRepoDefaultRoles()`). This is a
  value change, not just a rename: the expected roles differ (see
  behavioral change note above).
- `internal/cli/mint_test.go`: Update `config.DefaultAgentRoles()`
  assertion (~line 587) to `config.DefaultRoles()`. Assertion value
  must also change (per-repo role set, not per-org).
- `internal/cli/admin_test.go`: Update `config.DefaultAgentRoles()`
  reference (~line 51, `TestInstallCmd_Flags`). Lines 1774, 1811,
  1829, 1837 are inside per-org test functions (`TestRunDryRun`,
  `TestRunInstall`), deleted by PR 6.
- `internal/cli/github_test.go`: Update `config.DefaultAgentRoles()`
  references (~lines 62, 593).

**After merge:** Single `Config` type. No org-level config structures.

---

### PR 9: Remove per-org scaffold templates

**Scope:** Remove scaffold files only used by per-org.

**Delete files from `internal/scaffold/fullsend-repo/`:**

- `.github/workflows/dispatch.yml` — per-org dispatcher.
- `.github/workflows/code.yml` — per-org thin caller.
- `.github/workflows/triage.yml` — per-org thin caller.
- `.github/workflows/review.yml` — per-org thin caller.
- `.github/workflows/fix.yml` — per-org thin caller.
- `.github/workflows/retro.yml` — per-org thin caller.
- `.github/workflows/prioritize.yml` — per-org thin caller.
- `.github/workflows/repo-maintenance.yml` — enrollment reconciliation.
- `.github/workflows/prioritize-scheduler.yml` — per-org only (checks
  out `.fullsend` repo, uses dispatch role).
- `templates/shim-workflow-call.yaml` — per-org shim template.
- `config.yaml` — per-org enrollment config template (keep if also used
  as per-repo config template).
- `scripts/reconcile-repos.sh` — enrollment reconciliation script
  (contains `check_per_repo_guard` function); becomes dead code after
  per-org removal.
- `scripts/reconcile-repos-test.sh` — test script for the above.

**Rename files:**

- `templates/shim-per-repo.yaml` → `templates/shim.yaml`. Retains
  `install_mode: per-repo` (removed later in PR 14).

**`internal/scaffold/render.go`:**

- Remove thin-caller rendering code that becomes dead after template
  deletion: `thinStageWorkflows` registry (~line 24), `isThinStageCaller()`
  (~line 56), `thinStageName()` (~line 65), and the thin-caller branch
  in `RenderTemplate` (~line 43).

**Update `internal/scaffold/render_test.go`:**

- Remove or update the `install_mode: per-org` assertion (line 37) —
  per-org scaffold templates no longer exist after this PR.
- Remove 5 thin-caller test functions that exercise deleted templates:
  `TestRenderThinCallerNotVendored` (line 10),
  `TestRenderThinCallerVendoredPerOrg` (line 25),
  `TestRenderPrioritizeThinCallerVendored` (line 54),
  `TestThinStageWorkflowRegistryMatchesTemplates` (line 122),
  `TestRenderAllThinCallersFreeOfPlaceholders` (line 134).

**`internal/scaffold/scaffold.go`:**

- Remove `CustomizedDirs()` (org-level dirs rooted at `.`).
- Rename `PerRepoCustomizedDirs()` to `CustomizedDirs()`.
- Rename `PerRepoShimTemplate()` to `ShimTemplate()`.
- Remove `WalkFullsendRepo()` if it only walked org scaffold files.
- Remove any org-specific `embed` directives that reference deleted files.
- Remove `scripts/reconcile-repos.sh` from `executableFiles` map (~line 37).

**`Makefile` (must be in this PR to avoid CI breakage):**

- Remove or update `script-test` target that references
  `scripts/reconcile-repos-test.sh` (~line 114).

**Update callers:**

- `internal/cli/admin.go`: Update scaffold function calls.
- `internal/cli/github.go`: Update scaffold function calls.
- `internal/scaffold/installfiles.go`: Rename `PerRepoCustomizedDirs()`
  → `CustomizedDirs()` (lines 55, 79) and `PerRepoShimTemplate()` →
  `ShimTemplate()` (line 64). Remove `customizedDirsForPrefix()` (~line
  53) — after the rename both branches call the same function, making
  the prefix branching dead logic. Note: `CollectInstallFiles()` and
  `ManagedPaths()` are only called from `internal/layers/workflows.go`
  (deleted in PR 7); they become dead code after PR 7 merges. Evaluate
  whether to remove them as dead code or retain for future per-repo
  use. `installfiles_test.go` also calls `CollectInstallFiles()` and
  `ManagedPaths()` — these test callers survive PR 7 and would need
  updating if the functions are removed. This cleanup is only safe
  after PR 7 lands. **Deferred cleanup:** if kept, add a `// TODO:
  dead code after per-org removal — evaluate for removal or per-repo
  reuse` comment so reviewers know this is intentional.
- `internal/layers/workflows.go`: Already deleted in PR 7 (writes
  exclusively to `.fullsend` config repo). Verify no remaining callers.

**Test updates:**

- Update `internal/scaffold/render_test.go`: Rename `PerRepoShimTemplate()`
  → `ShimTemplate()` (line 41).
- Update `internal/scaffold/scaffold_test.go`: remove org scaffold tests,
  rename per-repo tests. Remove `scripts/reconcile-repos.sh` reference
  (line 78) and test block (lines 729–738). Remove 5 references to
  `shim-workflow-call.yaml` (lines 84, 107, 153, 699, 732). Remove
  `prioritize-scheduler.yml` references and related test functions
  (lines 96, 768, 774, 778, 792, 815, 828, 844).

**Note:** The per-org thin caller workflows deleted above
(`code.yml`, `fix.yml`, `prioritize.yml`, `retro.yml`, `review.yml`,
`triage.yml`) all hardcode `install_mode: per-org` — their deletion
removes these references.

**After merge:** Scaffold contains only per-repo templates.

---

### PR 10: Remove org-level forge methods

**Scope:** Clean up the forge interface. Depends on PR 6 (callers removed).

**`internal/forge/forge.go`:**

Remove from `Client` interface:

```go
// Org secrets (remove all 5):
CreateOrgSecret(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error
OrgSecretExists(ctx context.Context, org, name string) (bool, error)
DeleteOrgSecret(ctx context.Context, org, name string) error
SetOrgSecretRepos(ctx context.Context, org, name string, repoIDs []int64) error
GetOrgSecretRepos(ctx context.Context, org, name string) ([]int64, error)

// Org variables (remove all 5):
CreateOrUpdateOrgVariable(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error
OrgVariableExists(ctx context.Context, org, name string) (bool, error)
DeleteOrgVariable(ctx context.Context, org, name string) error
SetOrgVariableRepos(ctx context.Context, org, name string, repoIDs []int64) error
GetOrgVariableRepos(ctx context.Context, org, name string) ([]int64, error)

// Org installations — DO NOT remove:
// ListOrgInstallations is also used by per-repo install path
// (detectSharedApps in admin.go, findExistingInstallation and
// ensureInstalled in appsetup.go).
```

Remove `PerRepoGuardVar` constant and all its callers (these are
per-repo code paths, not removed by per-org CLI cleanup in PR 6):

- `admin.go`: guard checks (~lines 682, 826, 987) and variable
  creation in the per-repo install path. (Line 455 is inside the
  `enrollAll` branch, already deleted by PR 6.)
- `github.go`: per-repo variable map (~line 243), status display
  (~line 367), and `configKeyMapping` entry (~line 557).
- `admin_test.go`: guard variable assertions (~lines 2324, 2346).
- `github_test.go`: guard variable test cases (~lines 333, 341).
- `layers/enrollment.go`: guard check (~line 373) — already deleted
  in PR 7, listed here for completeness.
- `scaffold/fullsend-repo/scripts/reconcile-repos.sh`:
  `check_per_repo_guard` function (~lines 167–195) and callers
  (~lines 393, 516) — already deleted in PR 9, listed here for
  completeness.

The guard variable is no longer needed when per-org enrollment does not
exist. All callers are explicitly covered: per-repo paths in this PR,
`enrollment.go` in PR 7, and `reconcile-repos.sh` in PR 9.

Keep `ListOrgInstallations()` — used by per-repo app discovery
(`detectSharedApps`, `findExistingInstallation`, `ensureInstalled`).
Remove `ListOrgRepos()` — all callers (`admin.go` enrollment discovery,
`github.go` org setup, `dispatch.go` layer) are per-org code paths
removed in earlier PRs; no per-repo callers exist.
Keep `GetOrgPlan()` — used for plan detection.

**`internal/forge/github/github.go`:**

Remove implementations of the 10 removed interface methods:

- `CreateOrgSecret`, `OrgSecretExists`, `DeleteOrgSecret`,
  `SetOrgSecretRepos`, `GetOrgSecretRepos` (~200 lines)
- `CreateOrUpdateOrgVariable`, `OrgVariableExists`, `DeleteOrgVariable`,
  `SetOrgVariableRepos`, `GetOrgVariableRepos` (~200 lines)

Keep `ListOrgInstallations` implementation (retained in interface).

**`internal/forge/fake.go`:**

Remove:

- `OrgSecrets`, `OrgSecretRepoIDs` maps
- `OrgVariables`, `OrgVariableValues`, `OrgVariableRepoIDs` maps
- `OrgSecretRecord`, `OrgVariableRecord` structs
- `CreatedOrgSecrets`, `CreatedOrgVariables` slices
- All fake implementations of removed interface methods

**Test updates:**

- `internal/forge/github/github_test.go`: Remove org method test cases.
- Any test files that use fake org methods: update to remove.

**After merge:** Forge interface has no org-level secret/variable methods.
`ListOrgInstallations` retained for per-repo app discovery. ~400 lines
removed from GitHub implementation.

---

### PR 11: Simplify dispatch infrastructure

**Scope:** Remove org-level dispatch from provisioner. Depends on PR 6.

**`internal/dispatch/dispatch.go`:**

Remove from `Dispatcher` interface:

```go
OrgSecretNames() []string
OrgVariableNames() []string
```

Keep `Provision()`, `StoreAgentPEM()`, `Name()`.

**`internal/dispatch/gcf/provisioner.go`:**

- Retain `Config.GitHubOrgs` — used by per-repo install path
  (`admin.go` lines 713, 930, 952) for `EnsureOrgInMint()`. Consider
  renaming to `Org` (singular string) since per-repo always passes
  exactly one org.
- Remove `OrgSecretNames()` and `OrgVariableNames()` implementations.
- Remove PEM-sharing logic for shared public apps across orgs
  in per-org mode (per-repo uses role-scoped `StoreAgentPEM()` only).
- Simplify `ProvisionWIF()`: remove org-wide condition path
  (`attribute.repository_owner == "org"`), keep per-repo condition path
  (`attribute.repository == "owner/repo"`).
- Keep `RegisterPerRepoWIF()`, `StoreAgentPEM()`, `DiscoverMint()`,
  `GetExistingRoleAppIDs()`, `Provision()`.

**Test updates:**

- `internal/dispatch/gcf/provisioner_test.go`: Remove org-level test
  cases, keep per-repo WIF tests.

**After merge:** Dispatch provisioner handles only per-repo WIF.

---

### PR 12: Simplify appsetup

**Scope:** Simplify appsetup for per-repo only. Depends on PR 10
(forge interface updated).

**`internal/appsetup/appsetup.go`:**

- Keep `findExistingInstallation()` and `ensureInstalled()` — these use
  `ListOrgInstallations()` which is retained (app installation discovery
  is an org-level GitHub API operation regardless of fullsend install mode).
- Simplify `Run()`: remove any per-org-specific branching (e.g. org-level
  app creation paths that differ from per-repo). The shared app discovery
  flow (`detectSharedApps` → `findExistingInstallation`) works for both
  modes and is retained.
- Keep manifest flow (`runManifestFlow()`) for self-managed profile where
  users create their own apps.
- Keep `handleExistingApp()`, `recoverPEM()` — still useful for per-repo
  self-managed app recovery.
- Remove any org-only helpers that are not shared with per-repo path.

**Test updates:**

- `internal/appsetup/appsetup_test.go`: Remove org installation test
  cases.

**After merge:** Appsetup retains shared app discovery
(`findExistingInstallation`, `ensureInstalled` via `ListOrgInstallations`)
while per-org-specific branching is removed.

---

### PR 13: Update mint validation

**Scope:** Remove per-org workflow ref pattern from mint.

**`internal/mintcore/claims.go`:**

- Remove the `.fullsend` config repo prefix matching from
  `ValidateWorkflowRef()` (~lines 88–94).
- Keep:
  - `fullsend-ai/fullsend/.github/workflows/reusable-*.yml@*`
    (upstream reusable workflows)
  - `{owner}/{repo}/.github/workflows/*.yml@*` where repo is in
    `PER_REPO_WIF_REPOS` (per-repo workflows)
- Keep `ValidateOrgAllowed()` (~lines 59–67) and the `ALLOWED_ORGS` env
  var — they gate ALL token requests (per-org and per-repo) by checking
  `repository_owner` against the allowed list. This is required for
  multi-tenant mint security. Do not remove.
- Rename `PER_REPO_WIF_REPOS` to `WIF_REPOS` (no longer a
  "per-repo" distinction — it's the only mode).

**Test updates:**

- `internal/mintcore/claims_test.go`: Remove test cases for `.fullsend`
  workflow ref patterns. Update env var names if renamed.

**After merge:** Mint validates only upstream reusable workflow refs and
registered per-repo workflow refs.

---

### PR 14: Update reusable workflows and actions

**Scope:** Remove per-org branching from GitHub Actions workflows.

**`.github/workflows/reusable-dispatch.yml`:**

- Remove `install_mode` input (always per-repo behavior).
- Remove any conditional logic that branches on `install_mode`.

**`.github/workflows/reusable-triage.yml`, `reusable-code.yml`,
`reusable-review.yml`, `reusable-fix.yml`, `reusable-retro.yml`,
`reusable-prioritize.yml`:**

- Remove `install_mode` input from each workflow.
- Remove per-org checkout logic (two-checkout pattern: `.fullsend` repo +
  target repo).
- Keep per-repo checkout logic (single checkout, workspace root at
  `.fullsend/`).
- Remove per-org workspace prep path (root=`.`).
- Hardcode workspace root to `.fullsend/`.
- Remove `fullsend-dir` / `target-repo` branching based on install mode.

**`.github/actions/validate-enrollment/action.yml`:**

- Remove `install_mode` input.
- Remove per-org enrollment validation logic (config.yaml check).
- Either remove the action entirely (enrollment validation is always
  skipped in per-repo) or convert to a no-op placeholder for future use.

**`.github/workflows/fullsend.yaml`** (upstream repo shim):

- Convert from per-org format (calls `.fullsend/dispatch.yml`) to
  per-repo format (calls `reusable-dispatch.yml`).
- Or remove if `fullsend-ai/fullsend` does not use fullsend on itself.

**`internal/scaffold/fullsend-repo/templates/shim.yaml`** (renamed from
`shim-per-repo.yaml` in PR 9):

- Remove the `install_mode: per-repo` input (line 47) — no longer needed
  when all workflows assume per-repo.

**`install_mode` removal checklist (all 9 files):**

1. `.github/workflows/reusable-dispatch.yml`
2. `.github/workflows/reusable-triage.yml`
3. `.github/workflows/reusable-code.yml`
4. `.github/workflows/reusable-review.yml`
5. `.github/workflows/reusable-fix.yml`
6. `.github/workflows/reusable-retro.yml`
7. `.github/workflows/reusable-prioritize.yml`
8. `.github/actions/validate-enrollment/action.yml`
9. `internal/scaffold/workflow_call_alignment_test.go` — remove
   `install_mode` from the required-inputs assertion (~line 178);
   after this PR the reusable workflows no longer declare it.

**Verification:** PR 15's grep sweep (see `install_mode` pattern) covers
final verification that no `install_mode` references remain.

**After merge:** Reusable workflows have no per-org code paths. Single
checkout, single workspace root.

---

### PR 15: Remove per-org e2e tests and final cleanup

**Scope:** Final sweep. Depends on all prior PRs.

**Delete files:**

- `e2e/admin/admin_test.go` — `TestAdminInstallUninstall` (per-org
  lifecycle test).
- `e2e/admin/lock_test.go` — org pool locking (only needed for
  parallel per-org e2e tests).

**Simplify files:**

**`e2e/admin/cleanup.go`:**

- Remove `DeleteOrgSecret()` calls (legacy `FULLSEND_DISPATCH_TOKEN`
  cleanup).
- Remove `.fullsend` config repo deletion.
- Remove org variable cleanup.
- Keep repo-level cleanup utilities.

**`e2e/admin/testutil.go`:**

- Remove org-specific helpers if any exist.

**`internal/forge/forge.go`:**

- Remove `ConfigRepoName = ".fullsend"` constant if no longer referenced.
  (Check: is it used by per-repo for the `.fullsend/` directory name?
  If so, keep it or rename to `ConfigDirName`.)

**`Makefile`:**

- Update `e2e-test` target if test file paths changed.
- Remove any per-org-specific test targets.

**Grep sweep:**

Run a final grep for stale references:

```bash
grep -rn "per.org\|per_org\|PerOrg\|per-org" --include="*.go" --include="*.yml" --include="*.yaml" --include="*.md"
grep -rn "\.fullsend repo\|config repo\|ConfigRepo\|configrepo" --include="*.go"
grep -rn "enroll\|Enroll\|ENROLL" --include="*.go"
grep -rn "OrgConfig\|OrgSecret\|OrgVariable\|OrgInstall" --include="*.go"
grep -rn "install_mode\|install-mode\|installMode" --include="*.go" --include="*.yml" --include="*.yaml"
grep -rn "CONFIG_REPO_NAME\|ConfigRepoName" --include="*.ts" --include="*.tsx"
grep -rn "enrollment\|analyzeOrg\|configRepo\|orgConfigParse" --include="*.ts" --include="*.tsx"
grep -rn "PerRepo" --include="*.go"
```

Fix or remove any remaining references.

**TypeScript per-org cleanup (this PR):**

Delete or gut the following `web/admin/` files whose sole purpose is
per-org functionality:

- `src/lib/layers/configRepo.ts` and `configRepo.test.ts`
- `src/lib/layers/enrollment.ts` and `enrollment.test.ts`
- `src/lib/layers/dispatch.ts` and `dispatch.test.ts`
- `src/lib/layers/orgConfigParse.ts` and `orgConfigParse.test.ts`
- `src/lib/layers/analyzeOrg.ts` and `analyzeOrg.test.ts` — imports
  from `configRepo.ts`, `dispatch.ts`, and `enrollment.ts`; must be
  deleted with them to avoid TypeScript compilation failure.
- `src/lib/layers/constants.ts` — also imported by `secrets.ts`
  (`CONFIG_REPO_NAME`) and `orgListRow.ts`; update or remove these
  imports.
- `src/lib/orgs/orgListRow.ts` — also imported by
  `orgListAnalysisCache.ts` (type import) and
  `installReadinessProbes.test.ts` (type import); update downstream.
- `src/lib/orgs/installReadinessProbes.ts` — also imported by
  `auth/tokenStore.ts` (`clearInstallReadinessProbeCache`); update
  downstream.
- `src/lib/orgs/batchOrganizationsFullsendRepoGraphql.ts` (and its
  `.test.ts` companion) — queries for `.fullsend` config repos.

**Import dependency chains:** Deleting the files above without tracing
downstream imports will break `tsc --noEmit`. Run `tsc --noEmit` after
deletions to catch any remaining breakage. Key chains:
`constants.ts` → `secrets.ts`, `orgListRow.ts`;
`orgListRow.ts` → `orgListAnalysisCache.ts`;
`installReadinessProbes.ts` → `auth/tokenStore.ts`.

The grep patterns above cover these modules. Any remaining per-org
references surfaced by the sweep should be removed in this PR.

**After merge:** Codebase is fully per-repo. Tag as v2.0.

---

## Release checklist

### v1.x release (after Phase 1 merges)

- [ ] All per-org commands emit deprecation warnings
- [ ] Migration guide published at `docs/guides/admin/migrate-per-org-to-per-repo.md`
- [ ] `fullsend admin migrate` command functional
- [ ] Per-repo e2e test passing
- [ ] Migration e2e test passing
- [ ] Release notes mention per-org deprecation and link to migration guide

### v2.0 release (after Phase 2 merges)

- [x] Confirm ADR 0045 Phases 1–4 are merged and functional (harness
  `base` composition available) — prerequisite per ADR 0044 §Decision
- [ ] All per-org CLI commands removed
- [ ] `admin install` requires `owner/repo` format
- [ ] Layer stack simplified (no `ConfigRepoLayer`, `DispatchTokenLayer`, `EnrollmentLayer`)
- [ ] `OrgConfig` removed, `PerRepoConfig` renamed to `Config`
- [ ] Per-org scaffold templates deleted
- [ ] Forge interface has no org-level secret/variable methods
- [ ] Dispatch provisioner handles only per-repo WIF
- [ ] Mint validates only upstream + per-repo workflow refs
- [ ] Reusable workflows have no `install_mode` branching
- [ ] `validate-enrollment` action simplified or removed
- [ ] Per-org e2e tests removed, per-repo e2e tests passing
- [ ] Grep sweep clean — no stale per-org references
- [ ] Commit message: `feat(cli)!: remove per-org installation mode`
- [ ] `BREAKING CHANGE:` trailer in commit body: "Per-org installation
  mode removed. All `fullsend admin install --org`, `github enroll`,
  `github unenroll` commands are gone. Migrate to per-repo mode using
  `fullsend admin install <owner/repo>` (or use `fullsend admin migrate`
  on v1.x before upgrading). See
  docs/guides/admin/migrate-per-org-to-per-repo.md"
- [ ] Release notes: migration guide link, list of removed commands,
  upgrade instructions
