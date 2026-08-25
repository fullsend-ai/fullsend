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
3. Unenroll from per-org config

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

Converge repos to the desired state defined in a manifest. This is the primary command for managing per-repo installations — it handles adding repos to the manifest, provisioning new repos, repairing component drift (workflow, thin callers, variables, secrets), repairing scaffold content drift, and upgrading scaffold refs.

When the manifest file does not exist and positional repo arguments are
provided, `repos install` bootstraps a new manifest (`version: 1`),
adds the specified repos, and writes the file. The `--forge` flag is
required in this case. This enables a greenfield setup without running
`repos migrate` or manually creating the YAML first.

Runs in two phases:

1. **Manifest add** — repos specified as positional arguments that are not already in the manifest are added (`--forge` is required when the target platform cannot be inferred). Per-repo overrides (`--inference-region`, `--fullsend-ref`, `--mint-url`, `--allowed-remote-resources`, `--runtime`) are written to the manifest entry.
2. **Converge** — all manifest repos are converged through a unified probe → diff → apply pipeline. Repos with no components are freshly installed (scaffold files, variables, secrets). Repos with existing components are checked for drift (workflow, thin callers, variables, secrets — repaired automatically), scaffold content drift (repaired automatically), and scaffold ref drift (upgraded automatically).

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
| `--roles` | `triage,coder,review,fix,retro,prioritize` | Agent roles to install |
| `--direct` | `false` | Push scaffold directly to default branch (skip PR) |
| `--inference-project` | | GCP project ID for inference (written as `FULLSEND_GCP_PROJECT_ID` secret) |
| `--inference-project-number` | | Numeric GCP project number for WIF provider computation (auto-derived from `--inference-project` when omitted) |
| `--forge` | | Forge type for new repos (`github` or `gitlab`). Required when adding repos not already in the manifest; inferred from existing platform sections when unambiguous. |
| `--force` | `false` | Allow scaffold ref downgrades |
| `--inference-region` | | Per-repo GCP inference region override (default: global when `--inference-project` is set; install-time only, not stored in the manifest) |
| `--fullsend-ref` | | Per-repo fullsend workflow ref override |
| `--mint-url` | | Per-repo mint URL override |
| `--allowed-remote-resources` | | Per-repo allowed remote resources override |
| `--runtime` | | Agent runtime (`claude`, `pi`) recorded for repos this command adds; existing entries keep their `runtime` / `defaults.runtime` |
| `--gitlab-bot-token` | | GitLab bot PAT for free-tier instances that don't support project access tokens (env: `FULLSEND_GITLAB_BOT_TOKEN`) |

### GitLab bot token

For GitLab repos, `repos install` automatically creates a project access token and stores it as the `FULLSEND_FORGE_TOKEN` protected CI/CD variable. Creating project access tokens requires GitLab Premium or Ultimate.

On free-tier or Community Edition instances where project access tokens are not available, pass `--gitlab-bot-token` with a personal access token (PAT) that has `api` scope:

```bash
fullsend repos install group/project --forge gitlab --gitlab-bot-token glpat-xxxxxxxxxxxx
```

### Common workflows

Converge all repos from a manifest (provision new, repair component drift, repair scaffold content drift, upgrade refs):

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
fullsend repos install group/project --forge gitlab --direct
```

## `repos status`

Read-only comparison of the `repos.yaml` manifest against actual forge state. Reports installation status and configuration drift for each repo.

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

- **REPO** — `owner/repo` name
- **REF** — Current workflow ref. Named refs (tags, branches) display as-is (e.g., `v2.3.0`, `main`). When the ref is a commit SHA, shows a truncated 7-character SHA with the expected ref in parentheses (e.g., `6f8b968 (main)`).
- **STATUS** — `installed`, `not installed`, or `error`
- **DRIFT** — Fields that differ from the manifest, scaffold files whose template content has changed, orphan files or variables no longer in the managed set, or `none`

**JSON output** (`--json`) returns the full `StatusResult` object with per-repo details and aggregate summary counts.

### Exit codes

The command returns a non-zero exit code when any repo has drift, is not installed, or encountered an error. This makes it suitable for CI checks.

### Authentication

Requires a GitHub token via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`. For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass `--gitlab-token` to the `repos` command group.

## `repos uninstall`

Tear down fullsend from the specified repos and remove them from the manifest. By default, the command tears down first (deleting workflow files, variables, and secrets), then removes successfully-torn-down repos from the manifest. Partial failures leave the manifest entry intact so the user can retry.

GCP WIF pool/provider cleanup is handled separately via `inference deprovision`.

When multiple repos are targeted (via globs or explicit bulk lists), the command prompts for confirmation unless `--yes` is set.

```bash
fullsend repos uninstall acme/old-api
fullsend repos uninstall "acme/*" --yes
fullsend repos uninstall acme/old-api --dry-run
fullsend repos uninstall acme/old-api --manifest-only
fullsend repos uninstall acme/old-api --uninstall-only
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
| `defaults.allowed_remote_resources` | comma-separated URLs | HTTPS URLs agents may fetch at runtime |
| `defaults.runtime` | `claude` or `pi` | Agent runtime written as each repo's `runtime:` at install; a per-entry `runtime` overrides it (`none` stops the chain) |
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
- [Operations](../guides/getting-started/operations.md) — Day-2 administration
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
