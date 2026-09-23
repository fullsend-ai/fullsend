---
sidebar_label: fullsend repos
---

# fullsend repos

Manage per-repo installations across multiple orgs via a declarative `repos.yaml` manifest. Compare the manifest's desired state against actual forge state and report installation status and configuration drift.

## Global flags

These flags are inherited by all `repos` subcommands:

| Flag | Default | Description |
|------|---------|-------------|
| `--gitlab-token` | | GitLab personal or project access token (overrides `GITLAB_TOKEN` env var) |

## Commands

| Command | Description |
|---------|-------------|
| `fullsend repos migrate <org>` | Migrate an org from per-org to per-repo install |
| `fullsend repos install [repos...]` | Converge repos to the desired state defined in a manifest |
| `fullsend repos uninstall <repos...>` | Tear down fullsend from repos and remove from manifest |
| `fullsend repos status` | Compare manifest against actual repo state |
| `fullsend repos set-default <key> <value>` | Set or remove a platform-level default in repos.yaml |

## `repos migrate`

One-command migration from per-org to per-repo fullsend installation. For each repo enrolled in the org's per-org config:

1. Check inference WIF status; provision if needed
2. Install per-repo (scaffold workflows, variables, secrets) with config carried over from the org config
3. Remove the repository entry from per-org config

Successfully migrated repositories — and selected repositories already detected as per-repo installed — are deleted from the source `<org>/.fullsend/config.yaml`. They are not left as `enabled: false`, which would queue them for legacy offboarding. Failed, unselected, and pre-existing disabled entries are left unchanged, as is unrelated configuration. Dry runs do not modify the source config.

Generates a `repos.yaml` manifest reflecting the migrated state. When a `repos.yaml` already exists (e.g. from a previous `--repo`-filtered run), newly migrated repos are merged into it instead of overwriting it. Re-running after a partial migration picks up where it left off.

### Config carry-over

The migrate command maps portable fields from the org-level `config.yaml` into each repo's per-repo `.fullsend/config.yaml`:

| Org config field | Per-repo config field | Notes |
|---|---|---|
| `agents` | `agents` | Full deep copy including enabled state |
| `allowed_remote_resources` | `allowed_remote_resources` | Default resources are merged in |
| `create_issues` | `create_issues` | Deep copy of allow targets |
| `defaults.roles` | `roles` | Per-repo overrides from `repos.<name>.roles` take precedence |
| `defaults.runtime` | `runtime` | Only when explicitly set |
| `kill_switch` | `kill_switch` | Only when active |
| `defaults.status_notifications` | `status_notifications` | Deep copy |

The following org config fields have no per-repo equivalent and are **not** carried over. A warning is emitted for each:

- `defaults.max_implementation_retries`
- `defaults.auto_merge`

**Note:** Any automated process that keeps the org-level `config.yaml` up to date (e.g., agent source pinning) needs to be replicated for each migrated repo's `.fullsend/config.yaml`.

```bash
fullsend repos migrate <org> --project <gcp-project>
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | **(required)** | GCP project ID for inference |
| `--repo` | | Filter to specific repos (repeatable, supports globs) |
| `--dry-run` | `false` | Preview only |
| `--direct` | `false` | Push scaffold to default branch instead of PR |
| `--concurrency` | `4` | Parallel limit (1-32) |
| `-f`, `--manifest` | `repos.yaml` | Output path for generated repos.yaml |

### Required GCP permissions

- `roles/iam.workloadIdentityPoolAdmin`
- `roles/resourcemanager.projectIamAdmin`

## `repos install`

Converge repos to the desired state defined in a manifest. This is the primary command for managing per-repo installations — it handles adding repos to the manifest, provisioning new repos, repairing component drift (workflow, thin callers, variables, secrets, pipeline schedules), repairing scaffold content drift, and upgrading scaffold refs.

When the manifest file does not exist and positional repo arguments are
provided, `repos install` bootstraps a new manifest (`version: 1`),
adds the specified repos, and writes the file. The `--forge` flag is
required in this case. This enables a greenfield setup without running
`repos migrate` or manually creating the YAML first.

Runs in two phases:

1. **Manifest add** — repos specified as positional arguments that are not already in the manifest are added (`--forge` is required when the target platform cannot be inferred). Per-repo overrides (`--inference-region`, `--fullsend-ref`, `--mint-url`, `--allowed-remote-resources`, `--runtime`) are written to the manifest entry.
2. **Converge** — all manifest repos are converged through a unified probe → diff → apply pipeline. Repos whose shim workflow is not yet on the default branch are freshly installed (scaffold files, variables, secrets, and a declared configuration preset as `.fullsend/config.base.yaml`) onto the initialization branch (`fullsend/scaffold-install`). That includes a re-run while the initialization PR/MR is still open: variables and secrets may already exist from the first run, but the installer still updates the same initialization PR rather than opening a competing upgrade PR. Repos whose workflow is already on the default branch are checked for drift (workflow, thin callers, variables, secrets, pipeline schedules — repaired automatically), scaffold content drift (repaired automatically), declared configuration-preset drift against `.fullsend/config.base.yaml` (replaced wholesale; `.fullsend/config.yaml` is preserved), and scaffold ref drift (upgraded automatically).

```bash
fullsend repos install -f repos.yaml
fullsend repos install --dry-run
fullsend repos install acme/api acme/web
fullsend repos install "acme/*" --direct --concurrency 8
fullsend repos install acme/new-repo --forge github --direct
```

When repos are specified as positional arguments, only those repos are processed. Glob patterns (e.g. `acme/*`) are matched against manifest entries. When no repos are specified, all manifest repos are converged.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would change without making modifications |
| `--concurrency` | `4` | Max parallel operations (1-32) |
| `--roles` | `triage,coder,review,fix,retro,prioritize` | Agent roles to install. On a fresh install of a repo with a declared configuration preset, the preset's own roles take effect instead of this default unless `--roles` is explicitly passed on the command line. |
| `--direct` | `false` | Push scaffold directly to default branch (skip PR) |
| `--inference-project` | | GCP project ID for inference (written as `FULLSEND_GCP_PROJECT_ID` secret) |
| `--inference-wif-provider` | | Full WIF provider resource name (`projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}`); uses this provider for all repos instead of deriving per-repo providers. Project number is embedded in the path, so no auto-derivation is needed. |
| `--forge` | | Forge type for new repos (`github` or `gitlab`). Required when adding repos not already in the manifest; inferred from existing platform sections when unambiguous. |
| `--force` | `false` | Allow scaffold ref downgrades |
| `--inference-region` | | Per-repo GCP inference region override (default: global when `--inference-project` is set; install-time only, not stored in the manifest) |
| `--fullsend-ref` | | Per-repo fullsend workflow ref override |
| `--mint-url` | | Per-repo mint URL override |
| `--allowed-remote-resources` | | Per-repo allowed remote resources override |
| `--runtime` | | Agent runtime (`claude`, `pi`, `codex`) recorded for repos this command adds; existing entries keep their `runtime` / `defaults.runtime` |
| `--vendor` | `false` | Vendor binary, reusable workflows, actions, and agent content into each repo for offline CI. Can also be set via `defaults.vendor` or per-repo `vendor` in the manifest. By default, the binary is auto-resolved from `--fullsend-ref`; use `--fullsend-binary` or `--fullsend-source` to provide it explicitly. |
| `--fullsend-binary` | | Path to a pre-built Linux fullsend binary to upload when vendoring instead of auto-resolving (requires `--vendor`) |
| `--fullsend-source` | | Path to a fullsend source checkout for content and cross-compile instead of auto-detecting or fetching from GitHub (requires `--vendor`) |
| `--gitlab-url` | | GitLab instance URL (e.g. `https://gitlab.example.com`); sets `gitlab.url` in the manifest and implies `--forge=gitlab` when no forge is specified. Private-CA instances also need runner `tls-ca-file` / `CI_SERVER_TLS_CA_FILE` — see [Private CA (self-hosted GitLab)](../guides/getting-started/operations.md#private-ca-self-hosted-gitlab) |
| `--gitlab-bot-token` | | GitLab bot PAT for free-tier instances that don't support project access tokens (env: `FULLSEND_GITLAB_BOT_TOKEN`) |
| `--gitlab-role-migration` | | Emergency/administrative GitLab role-credential gate: `migrating`, `enforced`, `rollback`, or `disabled`. Ordinary unflagged `repos install` provisions role credentials and cuts over to `enforced` automatically. Passing `enforced` explicitly assumes drain the same way ordinary install does and does not require `--gitlab-role-cutover-drained`, unlike `--gitlab-role-cutover`. Use `rollback` or `disabled` only for explicit recovery (with `--gitlab-role-rollback-confirmed` when leaving `enforced`). |
| `--gitlab-role-registry` | | Path to administrator GitLab role registry JSON (custom roles: credential references and policy, never secret values). Written as the protected unmasked `FULLSEND_GITLAB_ROLE_REGISTRY` variable. |
| `--gitlab-role-token` | | Administrator-provided GitLab role PAT (`role=token`, repeatable) for free-tier enrollment or a custom `own` credential. Values are never logged. |
| `--gitlab-role-cutover` | `false` | Explicit retry of GitLab role cutover: verify every registered role, enable fail-closed `enforced` mode, and retire the shared `FULLSEND_FORGE_TOKEN`. Ordinary install already does this when roles are ready; this flag fails closed instead of deferring when cutover is not ready. |
| `--gitlab-role-cutover-drained` | `false` | Confirm that in-flight jobs using the shared credential have drained; required with `--gitlab-role-cutover`. Ordinary unflagged install treats drain as part of converging to the enforced desired state. |
| `--gitlab-role-rollback-confirmed` | `false` | Confirm reopening the shared-credential path when changing an enforced role gate to `rollback` or `disabled`; required for that reverse transition. |
| `--rotate-gitlab-roles` | `false` | Force-rotate GitLab role credentials even if they are not near expiry. Auto-rotation of expiring, expired, revoked, or unverified own-credential roles already runs during `repos install` when the gate is `migrating` or `enforced`. |
| `--rotate-gitlab-role` | | Rotate a specific GitLab role (repeatable). Default is all own-credential roles that are due. A `reuse` role follows its target. |

### GitLab bot token

For GitLab repos, `repos install` automatically creates a project access token at Developer (30) access with `api` scope and stores it as the `FULLSEND_FORGE_TOKEN` protected CI/CD variable, then provisions built-in Poller, Analyst, and Coder project access tokens (`FULLSEND_GITLAB_*_TOKEN`) on both fresh and existing shared-token installs. When every registered role is ready, the same unflagged run enables `FULLSEND_GITLAB_ROLE_MIGRATION=enforced` and retires `FULLSEND_FORGE_TOKEN`. If role credentials are only partially enrolled, cutover is deferred: the gate stays `migrating`, the shared credential is left in place (never recreated), and a later unflagged `repos install` retries. An explicit emergency rollback (`--gitlab-role-migration=rollback --gitlab-role-rollback-confirmed`) is the only supported way to reopen the shared-token path after enforced cutover. Custom roles are registered with `--gitlab-role-registry`; a custom role may reuse another registered credential or enroll its own token via `--gitlab-role-token`. When the gate is `migrating` or `enforced`, `fullsend poll` and `fullsend run` select the registered role credential instead of always reading `FULLSEND_FORGE_TOKEN`. The same `repos install` run rotates any own-credential role whose project access token is expiring, expired, revoked, or unverified: it creates a replacement PAT, writes it to the existing masked CI variable, and leaves the previous PAT active for 24 hours so in-flight jobs can finish. `--rotate-gitlab-roles` force-rotates every own-credential role; `--rotate-gitlab-role=poller` limits the run to one role. A failed rotation revokes only the unused replacement and leaves the previous secret in place. The shared `FULLSEND_FORGE_TOKEN` is never rotated or selected as a fallback. See [gitlab-role-credentials.md](../contributing/gitlab-role-credentials.md). Developer is sufficient because poller state lives on dedicated unprotected branches rather than Maintainer-only CI/CD variables. Creating project access tokens requires GitLab Premium or Ultimate. The token expiry is computed in UTC so a local-timezone date cannot produce a token that GitLab already considers expired (`active: false`).

Developer (30) access also depends on the default branch's protection settings: the poller creates pipelines via the API (`CreatePipeline`), which requires merge or push access to the protected default branch (see [ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)). GitLab's default "Protected" preset grants Developers merge access, so this works out of the box, but a repo whose branch protection restricts both merge and push to Maintainers will get a 403 on pipeline creation and dispatch will silently stop working. If your repo uses that stricter configuration, either grant Developers merge (or push) access to the default branch, or replace `FULLSEND_FORGE_TOKEN` with a Maintainer-level PAT manually in GitLab after install (converge does not rotate an existing token). `--gitlab-bot-token` will not help here: on Premium/Ultimate instances, `repos install` always creates its own Developer (30) project access token and ignores `--gitlab-bot-token` when that creation succeeds; the flag is only used as a fallback when project access tokens are unavailable (see below).

Install and converge also provision `FULLSEND_DISPATCH_SECRET` (a masked, protected CI/CD variable used to HMAC-sign dispatch variables and poller state) and create two unprotected poll-state branches (`fullsend-poll-state-slash` and `fullsend-poll-state-events`) holding an initial signed `state.json`. Existing `FULLSEND_LAST_POLL_AT_*` / `FULLSEND_LABEL_STATE` / `FULLSEND_DISPATCHED_KEYS_*` / `FULLSEND_FAILED_KEYS_*` values are migrated into those documents when present; otherwise each branch is seeded with an empty signed baseline. Already-written branch state is left untouched. After migrating, converge deletes any still-present retired poll-state CI/CD variables; they are treated as known-retired by the orphan detector (no warnings) and are not re-seeded on install.

On free-tier or Community Edition instances where project access tokens are not available, pass `--gitlab-bot-token` with a personal access token (PAT) that has `api` scope and at least Developer (30) access:

```bash
fullsend repos install group/project --forge gitlab --gitlab-bot-token glpat-xxxxxxxxxxxx
```

Project paths can include nested groups (e.g., `group/subgroup/project`):

```bash
fullsend repos install group/subgroup/project --forge gitlab --gitlab-bot-token glpat-xxxxxxxxxxxx
```

### Common workflows

Converge all repos from a manifest (provision new, repair component drift, repair scaffold content drift, refresh a declared configuration preset, upgrade refs):

```bash
fullsend repos install -f repos.yaml
```

Preview changes without modifying infrastructure:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Add a new repo to the manifest and install it:

```bash
fullsend repos install acme/new-repo --forge github --direct
```

Install specific repos:

```bash
fullsend repos install acme/api acme/web
```

Add a GitLab repo and install it:

```bash
fullsend repos install group/project --forge gitlab --gitlab-url https://gitlab.example.com --direct
```

### GitLab role cutover

Ordinary unflagged `repos install` provisions role credentials and, when
every registered role is ready, enables fail-closed `enforced` mode and
retires `FULLSEND_FORGE_TOKEN`. Drain in-flight shared-token jobs before
that converge; live GitLab ACL and branch-rule checks remain deployment
prerequisites. Partial enrollment defers cutover instead of failing the
install or recreating the shared credential.

To retry cutover explicitly (fail closed instead of deferring):

```bash
fullsend repos install group/project --forge gitlab --gitlab-role-cutover --gitlab-role-cutover-drained
```

Cutover verifies built-in and registered-role credentials and mappings,
requires project-token lifecycle inventory, enables fail-closed `enforced`
mode, and then retires `FULLSEND_FORGE_TOKEN`. If retirement fails, a gate
that this operation changed is restored to `migrating`; `--dry-run` performs
the checks without writing the gate or secrets.

In `fullsend repos status --json`, `gitlab_roles_ready` is true only when the
base role diagnosis, built-in role readiness, and every registered-role mapping
are all ready when role credentials are required (`migrating` or `enforced`).
Shared-token-only installations (`disabled` or `rollback`) retain the legacy
shared-credential readiness meaning and are not penalized for unprovisioned
role secrets; their diagnostics likewise do not include built-in or registered
role-readiness lines. In role-credential modes, the field may be false for an
installation that previously reported ready while a custom-agent mapping was
incomplete.

Leaving an enforced gate requires the explicit
`--gitlab-role-migration=rollback` (or `disabled`) plus
`--gitlab-role-rollback-confirmed` acknowledgement because it reopens the
shared-credential path. That emergency recovery path is separate from
ordinary unflagged converge.

## `repos status`

Read-only comparison of the `repos.yaml` manifest against actual forge state. Reports installation status and configuration drift for each repo, including declared configuration-preset drift against `.fullsend/config.base.yaml`.

```bash
fullsend repos status
fullsend repos status -f path/to/repos.yaml
fullsend repos status --repo acme/api --repo acme/web
fullsend repos status --repo "acme/*" --json
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |
| `--repo` | | | Filter to specific repos (repeatable, supports globs) |
| `--json` | | `false` | Emit JSON output instead of table |
| `--concurrency` | | `8` | Max parallel API calls |

### Output

**Table output** (default) shows per-repo status with columns:

- **REPO** — `owner/repo` name (GitLab repos with nested groups display as `group/subgroup/project`)
- **REF** — Current workflow ref. Named refs (tags, branches) display as-is (e.g., `v2.3.0`, `main`). When the ref is a commit SHA, shows a truncated 7-character SHA with the expected ref in parentheses (e.g., `6f8b968 (main)`).
- **STATUS** — `installed`, `not installed`, or `error`
- **DRIFT** — Fields that differ from the manifest, scaffold files whose template content has changed, orphan files or variables no longer in the managed set, or `none`

For GitLab repos, table and JSON output also include per-role credential
lifecycle diagnostic lines for roles needing attention (`expiring`,
`expired`, `revoked`, `unverified`, or `overlapping`); roles that are
`ok` or `unconfigured` do not get a diagnostic line. In enforced
role-migration mode, expired or revoked role credentials are reported
as `gitlab-role:<name>` drift. Status also appends built-in
Poller/Analyst/Coder and registered-role readiness checks (secret presence,
capability contract, and job-to-identity mapping). When token inventory is
available, expired, revoked, or unverified role credentials also downgrade
readiness; the base status path has no inventory and does not apply that
lifecycle refinement. A present shared
`FULLSEND_FORGE_TOKEN` is not treated as a substitute for a missing
built-in role. Registered roles with no agent mapping are also reported as
not ready. These checks do not change the migration gate or retire the shared
token.

**JSON output** (`--json`) returns the full `StatusResult` object with per-repo details and aggregate summary counts.

### Exit codes

The command returns a non-zero exit code when any repo has drift, is not installed, or encountered an error. This makes it suitable for CI checks.

### Authentication

Requires a GitHub token via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`. For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass `--gitlab-token` to the `repos` command group.

## `repos uninstall`

Tear down fullsend from the specified repos and remove them from the manifest. By default, the command tears down first (opening a PR to remove workflow files, then deleting variables and secrets via the API), then removes successfully-torn-down repos from the manifest. Partial failures leave the manifest entry intact so the user can retry.

File deletions (workflow YAML, `.fullsend/config.yaml`, and GitLab `.gitlab-ci.yml` unmerge) are delivered as a pull request unless `--direct` is set, matching `repos install`. Variable and secret deletions are API-only operations and always happen immediately. For GitLab repos, uninstall also deletes the `fullsend-poll-state-slash` and `fullsend-poll-state-events` branches (a missing branch is ignored so older installs still uninstall cleanly) while continuing to delete the retired poll-state CI/CD variables, the role-credential gate/registry/rotation-state variables, built-in and custom `FULLSEND_GITLAB_*_TOKEN` secrets, leftover `FULLSEND_FORGE_TOKEN`, and the corresponding `fullsend-bot` / `fullsend-poller` / `fullsend-analyst` / `fullsend-coder` / `fullsend-role-*` project access tokens. Token revocation is part of uninstall success: if listing or revoking those project access tokens fails, uninstall fails and the manifest entry is left in place for retry. Reinstall and converge do not revoke credentials that are already distributed.

Uninstall PR delivery intentionally reuses the same branch as `repos install`/`converge` (`fullsend/scaffold-install`), since already-deployed per-repo shims only exclude that branch name from dispatch. **Known limitation:** if an install PR is still open on that branch when uninstall runs (or an uninstall PR is open when install/converge runs), the existing PR is updated with the new commit but its title and body are left unchanged — the PR may show an install-oriented title while its diff now removes files, or vice versa. Check the PR's diff, not just its title, before merging when install and uninstall run close together against the same repo.

GCP WIF pool/provider cleanup for GitHub repos is handled separately via `inference deprovision`. This does not cover GitLab's shared `gitlab-oidc` WIF provider — for GitLab repos, see [Operations § Per-repo teardown](../guides/getting-started/operations.md#per-repo-teardown) step 6 to revoke that repo's WIF trust.

When multiple repos are targeted (via globs or explicit bulk lists), the command prompts for confirmation unless `--yes` is set.

```bash
fullsend repos uninstall acme/old-api
fullsend repos uninstall "acme/*" --yes
fullsend repos uninstall acme/old-api --dry-run
fullsend repos uninstall acme/old-api --manifest-only
fullsend repos uninstall acme/old-api --uninstall-only
fullsend repos uninstall acme/old-api --direct
```

For GitLab repos with nested group paths, use the full path:

```bash
fullsend repos uninstall group/subgroup/project
```

### Modes

| Flag | Teardown | Manifest removal |
|------|----------|------------------|
| *(default)* | Yes | Yes (only if teardown succeeds) |
| `--manifest-only` | No | Yes |
| `--uninstall-only` | Yes | No |

- **Default:** tear down + remove from manifest. Only repos whose teardown succeeds are removed from the manifest.
- **`--manifest-only`:** remove the manifest entry without tearing down the installation. Use when the repo is already deleted/transferred or was never successfully installed.
- **`--uninstall-only`:** tear down the installation but keep the manifest entry. Use for temporary teardown with intent to reinstall later.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be uninstalled without making changes |
| `--yes` | `false` | Skip confirmation prompt when multiple repos are targeted |
| `--direct` | `false` | Push file deletions to the default branch instead of opening a PR |
| `--concurrency` | `4` | Max parallel operations (1-32) |
| `--manifest-only` | `false` | Remove from manifest without tearing down |
| `--uninstall-only` | `false` | Tear down without removing from manifest |

## `repos set-default`

Set or remove a platform-level default in `repos.yaml`. An empty value removes the key. Creates the manifest with `version: 1` if the file does not exist.

```bash
fullsend repos set-default <key> <value>
fullsend repos set-default github.fullsend_ref v2.5.0
fullsend repos set-default github.mint_url ""   # removes the key
```

### Valid keys

| Key | Type | Description |
|-----|------|-------------|
| `defaults.allowed_remote_resources` | comma-separated URLs | URL prefixes allowed for remote resources (agents, policies, skills, plugins, profiles, providers, and base composition) |
| `defaults.runtime` | `claude`, `pi` or `codex` | Agent runtime written as each repo's `runtime:` at install; a per-entry `runtime` overrides it (`none` stops the chain) |
| `defaults.vendor` | `true` or `false` | Vendor fullsend binary and content into each repo for offline CI; per-entry `vendor` overrides it. Currently GitHub-only; GitLab CI templates do not yet reference the vendored binary. |
| `defaults.config_base.source` | local path or HTTPS URL | Configuration preset written as `.fullsend/config.base.yaml`; a per-entry `config_base.source` overrides it (`none` disables inheritance). A local path is resolved relative to `repos.yaml`'s directory and must not escape it; manifests loaded from an HTTPS URL must use an HTTPS preset URL. Fetch/validation semantics otherwise match `github setup --config`. |
| `defaults.config_base.sha256` | 64-character SHA-256 hex | Optional digest that validates the fetched preset; a per-entry `config_base.sha256` overrides it (`none` skips validation). Same semantics as `github setup --config-hash`. |
| `github.url` | URL | GitHub instance URL (default: `https://github.com`) |
| `github.mint_url` | URL | Token mint service URL (defaults to `https://mint.fullsend.sh` in public mode) |
| `github.mint_mode` | `public` or `private` | Controls the default mint URL: `public` defaults to `https://mint.fullsend.sh`; `private` requires an explicit `mint_url` (default: `public`) |
| `github.fullsend_ref` | ref string | Git ref to pin in scaffold workflow YAML |
| `gitlab.url` | URL | GitLab instance URL |
| `gitlab.fullsend_ref` | ref string | Git ref to pin in scaffold CI template files |
| `gitlab.runner_tags` | comma-separated tags | CI runner tags for routing agent jobs |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path to repos.yaml |

### Examples

Set the GitLab runner tags:

```bash
fullsend repos set-default gitlab.runner_tags fullsend-agent
```

Set multiple runner tags:

```bash
fullsend repos set-default gitlab.runner_tags "fullsend-agent,gpu-runner"
```

Remove runner tags:

```bash
fullsend repos set-default gitlab.runner_tags ""
```

Set the GitLab instance URL:

```bash
fullsend repos set-default gitlab.url https://gitlab.example.com
```

## See also

- [Getting Started](../guides/getting-started/) — Standard per-repo installation
- [Configuring GitLab](../guides/getting-started/configuring-gitlab.md) — GitLab getting-started guide
- [Operations](../guides/getting-started/operations.md) — Day-2 administration
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
