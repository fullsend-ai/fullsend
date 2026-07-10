---
title: "44. Deprecate per-org installation mode"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-architecture
  - security-threat-model
topics:
  - installation
  - per-org
  - per-repo
  - deprecation
  - migration
---

# 44. Deprecate per-org installation mode

Date: 2026-06-16

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

Deprecates the per-org installation mode established in
[ADR 0002](0002-initial-fullsend-design.md),
[ADR 0011](0011-admin-install-org-config-yaml-v1.md),
[ADR 0012](0012-admin-install-fullsend-repo-files-v1.md),
[ADR 0013](0013-admin-install-repo-enrollment-v1.md), and
[ADR 0014](0014-admin-install-github-apps-secrets-v1.md).

Per-repo installation mode ([ADR 0033](0033-per-repo-installation-mode.md))
becomes the sole supported installation model.

## Context

Fullsend's original installation model is per-org: `fullsend admin install <org>`
creates a dedicated `.fullsend` config repo, per-role GitHub Apps
([ADR 0007](0007-per-role-github-apps.md)), shim workflows in enrolled repos,
org-level variables and secrets, and a central token mint for OIDC-based
credential issuance ([ADR 0029](0029-central-token-mint-secretless-fullsend.md)).
This requires org admin access and assumes all enrolled repos share agent
configuration, credentials, and policies through the `.fullsend` config repo.

[ADR 0033](0033-per-repo-installation-mode.md) added a per-repo installation
mode where fullsend runs entirely within a single repository — no `.fullsend`
config repo, no cross-repo dispatch, no org-level secrets. Per-repo reuses the
reusable workflows from [ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md),
the centralized routing from [ADR 0034](0034-centralized-shim-routing-via-dispatch.md),
and the layered content resolution from [ADR 0035](0035-layered-content-resolution.md). The two modes coexist via a
`FULLSEND_PER_REPO_INSTALL` guard variable that prevents per-org enrollment
from overriding per-repo installations.

Maintaining both installation modes has become a significant source of
complexity. The per-org model carries substantial surface area:

1. **Dedicated `.fullsend` config repo** — a public repo that must exist for
   `workflow_call` to work across visibility boundaries, creating content
   exposure risk for private repos whose event payloads transit through it.

2. **Org-level infrastructure** — org variables (`FULLSEND_MINT_URL`), org
   secrets (legacy `FULLSEND_DISPATCH_TOKEN`), variable visibility management
   (`SetOrgVariableRepos`), and workarounds for dot-prefixed repo bugs.

3. **Enrollment machinery** — `config.yaml` enrollment lists, `repo-maintenance.yml`
   workflow dispatch, enrollment PRs, enable/disable commands, reconciliation
   scripts, and drift analysis.

4. **Three-level workflow nesting** — enrolled-repo shim (70 lines) calls
   `.fullsend/dispatch.yml` (370 lines) which calls thin callers (43-66 lines)
   which call upstream reusable workflows — compared to per-repo's two-level
   chain (shim calls `reusable-dispatch.yml` calls `reusable-{stage}.yml`).

5. **Scaffold duplication** — per-org installs a full set of scaffold files
   (`dispatch.yml`, thin callers for each stage, `repo-maintenance.yml`,
   `CODEOWNERS`, composite actions) into the `.fullsend` repo, creating
   drift risk despite ADR 0031's effort to minimize it.

6. **Dual code paths** — CLI commands (`admin install`, `admin uninstall`,
   `admin enable/disable repos`, `github setup`, `github enroll/unenroll`),
   layer implementations (`ConfigRepoLayer`, `DispatchTokenLayer`,
   `EnrollmentLayer`), config structures (`OrgConfig` vs `PerRepoConfig`),
   scaffold templates, shim templates, WIF provisioning, and e2e tests all
   branch on installation mode.

7. **Higher OAuth scope requirements** — per-org install requires `admin:org`
   scope to manage org-level variables, secrets, and app installations.
   Per-repo requires only `repo` and `workflow` scopes when reusing existing
   apps.

Meanwhile, the per-repo model — built on top of the same reusable workflows,
token mint, and content resolution infrastructure — achieves feature parity
with per-org while eliminating these complexities. The key enabling change was
ADR 0031's publication of reusable workflows from `fullsend-ai/fullsend`:
once all agent logic lives in upstream reusable workflows, the `.fullsend`
config repo becomes an unnecessary indirection layer.

[ADR 0045](0045-forge-portable-harness-schema.md) further erodes the
per-org model's remaining advantages. It moves agent identity (`role` and
`slug`) out of `config.yaml`'s centralized `agents:` block and into
individual harness files, making `config.yaml` "purely operational" (kill
switch, dispatch mode, URL allowlists). Agents are discovered by file
existence (`harness/*.yaml`), not by central registration — removing one
of per-org's key selling points as the single source of truth for agent
definitions. ADR 0045 also introduces harness composition via a `base`
field: any repo can reference an upstream harness by URL and override only
the fields that differ. This gives per-repo installations the same
customization power that previously required a centralized `.fullsend`
config repo — a thin wrapper with `base:` pointing to an upstream harness
gets org defaults with local overrides. Cross-org sharing works the same
way, without a shared config repo.

[ADR 0038](0038-universal-harness-access.md) complements this by making
the `.fullsend` config repo unnecessary as a resource distribution point.
Harness resources (agents, skills, policies) can be referenced by URL
with mandatory SHA256 integrity hashes rather than requiring local copies
in the `.fullsend` directory structure. A per-repo harness can reference
`https://raw.githubusercontent.com/fullsend-ai/library/.../agents/code.md#sha256=abc123...`
directly — no config repo needed to host shared resources. Combined with
ADR 0045's `base` composition, ADR 0038 fully replaces the config repo's
role as both a customization hub (ADR 0045) and a resource distribution
point (ADR 0038).

One governance consideration: ADR 0038's `allowed_remote_resources`
allowlist in `config.yaml` controls which URL domains are trusted for
remote resource fetches. In per-org mode, this allowlist lives in the
`.fullsend` repo — a single trust boundary governing all enrolled repos.
In per-repo mode, each repo controls its own allowlist independently,
meaning an org cannot centrally enforce which remote resource domains are
trusted. This is a specific instance of the broader "no centralized policy
enforcement" trade-off discussed below.

## Pros and cons of removing per-org

### Pros

1. **Eliminates content exposure risk for private repos.** Per-org routes
   event payloads through the public `.fullsend` config repo. Workflow run
   logs and event context are visible in the public repo's Actions tab. Per-repo
   keeps all event data within the target repo's own context — the primary
   security benefit and the reason per-repo is already the recommended default
   for private repos.

2. **Reduces CLI and layer code by ~40%.** Removing per-org eliminates the
   `ConfigRepoLayer`, `DispatchTokenLayer`, `EnrollmentLayer`, org-level
   variable/secret management and repo listing in the forge interface (11 methods),
   enrollment commands (`enable/disable repos`, `enroll/unenroll`), the
   uninstall command, the `OrgConfig` struct, per-org scaffold templates,
   `dispatch.yml`, thin caller workflows, and the e2e test suite.

3. **Simplifies the workflow nesting model.** Per-repo uses two levels of
   `workflow_call` (shim → `reusable-dispatch.yml` → `reusable-{stage}.yml`)
   vs per-org's three levels (shim → `dispatch.yml` → thin caller →
   `reusable-{stage}.yml`). Fewer levels mean simpler debugging, faster
   workflow startup, and more headroom under GitHub's 4-level nesting limit.

4. **Removes the enrollment system.** Per-org requires explicit repo
   enrollment via `config.yaml`, reconciliation scripts, enrollment PRs,
   and drift analysis. Per-repo repos are always self-enrolled — no
   enrollment state to manage, no drift to detect, no reconciliation to run.

5. **Lowers the scope floor.** Per-repo installs reusing existing apps need
   only `repo` and `workflow` OAuth scopes. Per-org always requires
   `admin:org` for org-level variable and secret management.

6. **Self-contained repos.** Each repo carries its own config (`.fullsend/`
   directory), workflow (`.github/workflows/fullsend.yml`), and customizations
   (`.fullsend/customized/`). No cross-repo dependencies, no shared state,
   no coordination through a central config repo.

7. **Unified distribution model.** Reusable workflows
   ([ADR 0031](0031-reusable-workflows-for-action-installed-distribution.md))
   become the sole distribution mechanism. Infrastructure patches ship once
   in `fullsend-ai/fullsend` and propagate to all repos — no scaffold
   re-install, no thin-caller drift.

8. **Simpler credential model.** Per-repo WIF providers are scoped to
   individual repos (`attribute.repository == "owner/repo"`) vs per-org's
   org-wide scope (`attribute.repository_owner == "org"`). A credential
   compromise in per-repo affects only that single repo.

### Cons

1. **No centralized policy enforcement.** Per-org provides a single `.fullsend`
   config repo where org admins can enforce uniform agent behavior, policies,
   and roles across all enrolled repos. Per-repo delegates policy to each
   repo independently — an org cannot guarantee all repos use the same
   sandbox policies, agent prompts, or skill sets. Organizations needing
   uniform policy must rely on CODEOWNERS conventions and organizational
   processes rather than a centralized config repo. This is partially
   mitigated by [ADR 0045](0045-forge-portable-harness-schema.md)'s `base`
   composition: repos can reference shared upstream harnesses by URL and
   override only what differs, providing a distribution mechanism for org
   defaults without requiring a `.fullsend` config repo.

2. **Weaker config governance model.** In per-org, agent config lives in a
   separate `.fullsend` repo with its own CODEOWNERS, isolated from code
   contributors. In per-repo, `.fullsend/` config lives alongside code —
   a code contributor could modify agent behavior in a PR. CODEOWNERS
   entries on `.fullsend/` and `.github/workflows/fullsend.yml` mitigate
   this, but the separation is weaker than a dedicated config repo.

3. **Per-repo setup overhead for large orgs.** Organizations with many
   repos must run `fullsend admin install <owner/repo>` for each one
   individually. Per-org's `--enroll-all` flag enrolled all repos in a
   single command. Tooling (scripts, CI automation) can mitigate this, but
   the out-of-the-box experience for large-scale adoption is less
   streamlined.

4. **Credential separation collapses.** In per-org mode, the `.fullsend`
   config repo holds credentials separate from enrolled repos — a
   compromised code contributor in an enrolled repo cannot access PEM
   secrets in `.fullsend`. In per-repo, the repo that triggers workflows
   also holds repo-level secrets (`FULLSEND_GCP_WIF_PROVIDER`,
   `FULLSEND_GCP_PROJECT_ID`). The token mint mitigates this (PEMs remain
   in Secret Manager), but repo-level secrets are co-located with code.

5. **No single source of truth for enrollment.** Per-org's `config.yaml`
   provides a clear, queryable list of which repos have fullsend enabled
   and what roles they use. Without it, discovering fullsend-enabled repos
   requires scanning for `FULLSEND_PER_REPO_INSTALL` variables or
   `fullsend.yml` workflow files across all repos.

6. **App installation still requires org admin.** Even though per-repo
   removes the need for org admin to run `fullsend admin install`, an
   org admin must still approve GitHub App installations on individual
   repos. This is a GitHub platform constraint, not a per-org vs per-repo
   distinction, but it limits the "no org admin needed" benefit.

7. **Migration burden for existing per-org users.** All existing per-org
   installations must migrate to per-repo. This involves per-repo CLI
   runs, workflow changes in each enrolled repo, variable/secret updates,
   and cleanup of the `.fullsend` config repo and org-level resources.

### User impact

**Org admins managing per-org installations** are the most affected. They
must execute a migration for each enrolled repo: run
`fullsend admin install <owner/repo>`, verify the per-repo shim is active,
and remove per-org enrollment. After all repos migrate, the `.fullsend`
config repo and org-level variables can be cleaned up.

**Per-repo users** are unaffected — their installation model becomes the
standard.

**Users who valued centralized policy** lose the `.fullsend` config repo
as a single policy enforcement point. They can replicate policy consistency
through [ADR 0045](0045-forge-portable-harness-schema.md)'s `base`
composition (per-repo harnesses reference shared upstream harnesses by URL,
inheriting org defaults and overriding only what differs), organizational
processes (e.g., a shared `.fullsend/customized/` template copied to each
repo), or CI checks that validate repo-level configs against an org
standard.

**Users with private repos** benefit — per-repo is already the recommended
default for private repos, and removing per-org eliminates the risk of
accidentally using the less-secure per-org model.

## Options

### Option A: Deprecate and remove per-org (chosen)

Deprecate per-org with a migration period, then remove. Per-repo becomes
the sole installation model. Simplifies the codebase, eliminates
dual-mode maintenance, and removes the content exposure risk inherent in
per-org's public config repo.

### Option B: Maintain both modes indefinitely

Continue supporting per-org and per-repo as equal alternatives.

**Rejected**: The ongoing maintenance cost of dual code paths, dual
testing, dual documentation, and coexistence logic (guard variables,
enrollment skipping) outweighs the benefit. Per-repo achieves feature
parity with per-org while being simpler and more secure by default. The
centralized policy benefit of per-org can be replicated through
organizational processes without fullsend-level infrastructure.

### Option C: Merge per-org into per-repo with org-wide automation

Build an org-level automation layer on top of per-repo: a script or
CI workflow that runs `fullsend admin install <owner/repo>` for every
repo in an org, enforces consistent `.fullsend/customized/` configs,
and provides a centralized dashboard.

**Deferred**: This could be built as a follow-up after per-org removal,
but it is not a prerequisite. The core decision (per-repo as sole mode)
stands independently. An org-wide automation tool is an enhancement,
not a replacement for per-org's config repo model.

## Decision

### 1. Deprecation plan

Deprecate the per-org installation mode in two phases:

**Phase 1 — Deprecation (v1.x release):**

- Add deprecation warnings to all per-org CLI commands (`admin install <org>`,
  `admin uninstall`, `admin enable/disable repos`, `github enroll/unenroll`,
  `github setup <org>`).
- Warnings direct users to `fullsend admin install <owner/repo>` and link
  to migration documentation.
- Per-org commands continue to function but emit a deprecation notice on
  every invocation.
- Update all user-facing documentation to present per-repo as the primary
  and recommended installation mode.
- Publish a migration guide (see section 3 below).
- Migrate existing per-org e2e tests to cover per-repo mode, ensuring
  test coverage is preserved before per-org code paths are removed.

**Phase 2 — Removal (v2.0 release, breaking change):**

**Prerequisite:** [ADR 0045](0045-forge-portable-harness-schema.md) Phases 1–2
must be complete before Phase 2 begins — harness composition via `base` URLs
replaces the `.fullsend` config repo as the mechanism for sharing org defaults.
ADR 0045 is accepted and its implementation (Phases 1–4) is complete.

- Remove all per-org CLI commands and flags (`--enroll-all`, `--enroll-none`).
- Remove per-org layers (`ConfigRepoLayer`, `DispatchTokenLayer`,
  `EnrollmentLayer`).
- Remove per-org config structures (`OrgConfig`, `NewOrgConfig`,
  `ParseOrgConfig`, `EnabledRepos`, `DisabledRepos`).
- Remove per-org scaffold templates (`dispatch.yml`, thin callers,
  `shim-workflow-call.yaml`, `repo-maintenance.yml`).
- Remove org-level forge methods (`CreateOrUpdateOrgVariable`,
  `OrgVariableExists`, `DeleteOrgVariable`, `SetOrgVariableRepos`,
  `GetOrgVariableRepos`, `CreateOrgSecret`, `OrgSecretExists`,
  `DeleteOrgSecret`, `SetOrgSecretRepos`, `GetOrgSecretRepos`,
  `ListOrgRepos`). Keep `ListOrgInstallations` (now on `GitHubExtensions`,
  per-repo app discovery) and `GetOrgPlan` (plan detection).
- Remove the `FULLSEND_PER_REPO_INSTALL` guard variable and all
  guard-checking logic (no longer needed when per-org enrollment
  does not exist).
- Convert per-org e2e tests to per-repo mode (preserving coverage)
  and remove org pool locking.
- Remove `fullsend` dispatch role from default agent roles (per-repo
  already excludes it).
- Remove the `admin uninstall` command (per-repo cleanup is
  repo-level: delete the workflow file, remove variables/secrets).
- Simplify the `admin install` command to accept only `<owner/repo>`
  format.
- Tag as v2.0 with `BREAKING CHANGE:` trailer per
  [CONTRIBUTING.md](../../CONTRIBUTING.md).

### 2. Implementation plan

The detailed, per-file implementation plan is maintained in a companion
document: [`docs/plans/deprecate-per-org-install.md`](../plans/deprecate-per-org-install.md).
It contains a 15-PR dependency graph, per-function change lists, and a
release checklist. The summary below captures the high-level ordering.

**Phase 1** (5 PRs):

1. Add deprecation warnings to all per-org CLI paths.
2. Update user-facing documentation to present per-repo as primary.
3. Publish migration guide.
4. Add `fullsend admin migrate` command (with `--all` and `--dry-run`).
5. Add migration e2e test.

**Phase 2** (10 PRs, ordered by dependency — see plan for full graph):

- PRs 6–7: Remove per-org CLI commands and layer stack
  (`ConfigRepoLayer`, `DispatchTokenLayer`, `EnrollmentLayer`).
- PR 7: Remove per-org harness wrappers and workflow layer.
- PR 8: Remove per-org config structures; rename `PerRepoConfig` →
  `Config`, `PerRepoDefaultRoles()` → `DefaultRoles()`.
- PR 9: Remove per-org scaffold templates; rename per-repo scaffolds.
- PR 10: Remove org-level forge methods (10 secret/variable methods,
  `ListOrgRepos`); remove `PerRepoGuardVar`; keep
  `ListOrgInstallations` (now on `GitHubExtensions`, per-repo app
  discovery) and `GetOrgPlan`.
- PR 11: Simplify dispatch provisioner; retain `Config.GitHubOrgs`
  (needed by `EnsureOrgInMint`).
- PR 12: Simplify appsetup; remove per-org branching.
- PR 13: Update mint validation (`ValidateWorkflowRef`); keep
  `ValidateOrgAllowed`.
- PR 14: Remove `install_mode` from reusable workflows and actions.
- PR 15: Remove per-org e2e tests, TypeScript cleanup, final grep sweep.

### 3. Migration plan for existing per-org users

#### Prerequisites

- Identify all repos enrolled in per-org by reading
  `.fullsend/config.yaml` (`repos:` section, `enabled: true`).
- Ensure token mint is deployed and accessible (same mint serves
  both per-org and per-repo).
- Ensure per-role GitHub Apps are installed on each target repo
  (or are shared public apps already installed at the org level).
- Have `repo` and `workflow` OAuth scopes for the migration token.

#### Per-repo migration steps (for each enrolled repo)

1. **Run per-repo install.**
   ```bash
   fullsend admin install <owner>/<repo> \
     --mint-url <existing-mint-url> \
     --inference-project <existing-project> \
     --skip-app-setup \
     --skip-mint-deploy
   ```
   This creates the per-repo shim workflow
   (`.github/workflows/fullsend.yml`), `.fullsend/config.yaml`,
   `.fullsend/customized/` directories, and sets repo-level
   variables/secrets.

   Alternatively, use the migration command:
   ```bash
   fullsend admin migrate <org> <repo>
   ```

2. **Copy customizations.** If the org `.fullsend` config repo has
   customizations in `customized/` (agent overrides, policies, skills,
   scripts), copy them to the target repo's `.fullsend/customized/`
   directory:
   ```bash
   # Clone the .fullsend config repo
   git clone https://github.com/<org>/.fullsend /tmp/fullsend-config
   # Copy customizations to target repo
   cp -r /tmp/fullsend-config/customized/* <target-repo>/.fullsend/customized/
   # Commit and push
   cd <target-repo>
   git add .fullsend/customized/
   git commit -m "feat: migrate fullsend customizations from per-org config"
   git push
   ```

3. **Verify per-repo installation.** Create a test issue or PR to
   confirm the per-repo shim triggers correctly and agents respond:
   ```bash
   gh issue create --repo <owner>/<repo> \
     --title "Test: verify per-repo fullsend migration" \
     --body "Testing per-repo installation after migration from per-org."
   ```
   Verify triage agent responds with a comment.

4. **Remove per-org enrollment.** After verifying per-repo works:
   ```bash
   fullsend admin disable repos <org> <repo>
   ```
   This removes the repo from `.fullsend/config.yaml` and triggers a
   PR to remove the per-org shim workflow from the repo.

5. **Merge the unenrollment PR.** The `repo-maintenance.yml` workflow
   creates a PR removing the per-org shim. Merge it. The per-repo shim
   (installed in step 1) takes over.

6. **Add CODEOWNERS protection.** Add entries to the repo's `CODEOWNERS`
   to protect per-repo config files:
   ```
   .github/workflows/fullsend.yml  @<admin-team>
   .fullsend/                      @<admin-team>
   ```

#### Org-level cleanup (after all repos are migrated)

7. **Delete org-level variables.**
   ```bash
   gh variable delete FULLSEND_MINT_URL --org <org>
   gh variable delete FULLSEND_GCP_REGION --org <org>
   ```

8. **Delete legacy org secrets** (if any remain from PAT dispatch mode):
   ```bash
   gh secret delete FULLSEND_DISPATCH_TOKEN --org <org>
   ```

9. **Archive or delete the `.fullsend` config repo.**
   ```bash
   # Archive (preserves history)
   gh repo archive <org>/.fullsend
   # Or delete
   gh repo delete <org>/.fullsend --yes
   ```

10. **Optionally clean up GitHub Apps.** Per-role apps
    (`fullsend-ai-triage`, `fullsend-ai-coder`, etc.) can remain
    installed — they are still used by per-repo installations via the
    token mint. Only delete apps that are no longer needed.

#### Rollback

If a per-repo installation fails during migration:

1. The per-org enrollment is still active (step 4 has not run yet).
2. Delete the per-repo shim: `git rm .github/workflows/fullsend.yml`
   and push.
3. The per-org shim (still in place) continues to function.
4. Remove per-repo variables:
   ```bash
   gh variable delete FULLSEND_PER_REPO_INSTALL --repo <owner>/<repo>
   ```

Migration is designed to be repo-by-repo with verification between each
repo. An org can run per-org and per-repo simultaneously (the coexistence
model from ADR 0033) throughout the migration window.

#### Automation

For orgs with many repos, the `fullsend admin migrate --all` command
automates the migration:

```bash
fullsend admin migrate <org> --all \
  --mint-url <existing-mint-url> \
  --inference-project <existing-project>
```

This reads `config.yaml`, iterates over all `enabled: true` repos,
runs per-repo install on each, verifies the shim is active, and
disables per-org enrollment. It processes repos sequentially (per-repo
WIF registration is not safe for concurrent calls) and reports
progress.

## Consequences

### Positive

- **Single code path.** All installation, setup, and maintenance code
  follows one model. No mode detection, no guard variables, no
  enrollment machinery.
- **Smaller codebase.** Removes ~40% of the CLI and layer code,
  simplifies the forge interface, eliminates per-org scaffold templates.
- **Better security default.** Per-repo eliminates the content exposure
  risk of routing event payloads through a public `.fullsend` config
  repo. Per-repo WIF providers are scoped to individual repos rather
  than entire orgs.
- **Simpler onboarding.** New users learn one installation model. The
  SaaS profile (shared public apps + upstream reusable workflows) can
  onboard a repo in under 15 minutes.
- **Faster infrastructure patches.** Reusable workflows are the sole
  distribution mechanism — patches ship once in `fullsend-ai/fullsend`
  and propagate to all repos with zero per-repo action.
- **Reduced GitHub scope requirements.** Per-repo installs reusing
  existing apps need only `repo` and `workflow` scopes.

### Negative

- **Migration effort for existing users.** Every per-org installation
  must be migrated repo-by-repo. The `fullsend admin migrate` command
  and `--all` flag reduce manual work, but verification is still
  per-repo.
- **No centralized policy enforcement.** Org admins lose the `.fullsend`
  config repo as a single point of policy control. Consistent policy
  can be achieved through ADR 0045's `base` harness composition (repos
  reference shared upstream harnesses by URL), organizational processes,
  shared templates, or future org-wide automation tooling.
- **Per-repo setup overhead at scale.** Large orgs must run
  `fullsend admin install <owner/repo>` for each repo. The migration
  command's `--all` flag and scripting mitigate this for initial setup,
  but each new repo requires its own install invocation.
- **Redundant org-level credentials.** Per-repo install creates
  repo-level copies of shared credentials (`FULLSEND_MINT_URL`,
  `FULLSEND_GCP_REGION`, etc.) that are identical across repos in
  an org. GitHub natively supports org-level secret/variable
  inheritance, but the current per-repo install flow does not
  leverage it. A future `fullsend admin setup-org-credentials`
  command or `--org-level-credentials` flag could detect and reuse
  existing org-level credentials without requiring the per-org
  installation model (see Option C).
- **Breaking change.** v2.0 removes per-org commands. Users who have
  not migrated will encounter errors. The deprecation period (Phase 1)
  provides advance notice.

### Risks

Ordered by the project's threat priority:

- **Migration window exposure.** During migration, some repos run
  per-org and others run per-repo. The coexistence model (ADR 0033's
  guard variable) handles this, but org admins must complete migration
  before the v2.0 removal deadline.
- **Incomplete migration.** Users who ignore deprecation warnings and
  upgrade to v2.0 without migrating will lose per-org functionality.
  The v2.0 release notes and CLI error messages must clearly direct
  users to the migration guide.
- **Policy drift.** Without centralized config, repos may diverge in
  agent behavior, sandbox policies, and skills. ADR 0045's `base`
  harness composition provides a distribution mechanism for shared
  defaults, but orgs must still enforce that repos use the shared
  `base` reference rather than diverging independently.

## References

- [ADR 0002: Initial fullsend design](0002-initial-fullsend-design.md) — original per-org model
- [ADR 0007: Per-role GitHub Apps](0007-per-role-github-apps.md) — app model preserved in per-repo
- [ADR 0011: Admin install org config YAML v1](0011-admin-install-org-config-yaml-v1.md) — deprecated by this ADR
- [ADR 0012: Admin install fullsend repo files v1](0012-admin-install-fullsend-repo-files-v1.md) — deprecated by this ADR
- [ADR 0013: Admin install repo enrollment v1](0013-admin-install-repo-enrollment-v1.md) — deprecated by this ADR
- [ADR 0014: Admin install GitHub Apps secrets v1](0014-admin-install-github-apps-secrets-v1.md) — deprecated by this ADR
- [ADR 0029: Central token mint](0029-central-token-mint-secretless-fullsend.md) — shared infrastructure, preserved
- [ADR 0031: Reusable workflows](0031-reusable-workflows-for-action-installed-distribution.md) — sole distribution model after removal
- [ADR 0033: Per-repo installation mode](0033-per-repo-installation-mode.md) — becomes the standard
- [ADR 0034: Centralized shim routing](0034-centralized-shim-routing-via-dispatch.md) — `reusable-dispatch.yml` replaces per-org `dispatch.yml`
- [ADR 0035: Layered content resolution](0035-layered-content-resolution.md) — `.fullsend/` directory replaces `.fullsend` repo
- [ADR 0038: Universal harness access](0038-universal-harness-access.md) — URL-based resource references replace config repo as resource distribution point
- [ADR 0045: Forge-portable harness schema](0045-forge-portable-harness-schema.md) — agent identity moves to harness files; `base` composition replaces centralized config repo overrides
- [Implementation plan](../plans/deprecate-per-org-install.md) — PR dependency graph, per-file changes, release checklist
