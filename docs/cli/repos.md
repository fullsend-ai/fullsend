---
sidebar_label: fullsend repos
---

# fullsend repos

Manage per-repo installations across multiple orgs via a declarative `repos.yaml` manifest. Compare the manifest's desired state against actual forge state and report installation status and configuration drift.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend repos init <org\|owner/repo>` | Generate a repos.yaml manifest by discovering existing installations |
| `fullsend repos install [repos...]` | Install fullsend on uninstalled manifest repos |
| `fullsend repos add <repos...>` | Add repo entries to a repos.yaml manifest |
| `fullsend repos remove <repos...>` | Remove repo entries from a repos.yaml manifest |
| `fullsend repos uninstall <repos...>` | Tear down fullsend from specific repos |
| `fullsend repos status` | Compare manifest against actual repo state |
| `fullsend repos diff` | Show configuration drift between manifest and actual state |
| `fullsend repos sync` | Reconcile configuration drift for installed repos |
| `fullsend repos upgrade [repos...]` | Upgrade scaffold shim ref across repos |
| `fullsend repos upgrade-mint` | Verify the token mint deployment matches the manifest |

## `repos init`

Discovers existing fullsend installations (per-repo and per-org) and generates a `repos.yaml` manifest reflecting their current state. Supports greenfield onboarding and migration from existing installations.

```bash
fullsend repos init <org> --all --mint-project <PROJECT> --inference-project <PROJECT>
```

Single-repo mode:

```bash
fullsend repos init <owner/repo> --mint-project <PROJECT>
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--output`, `-o` | `repos.yaml` | Output path (use `-` for stdout) |
| `--repos` | | Comma-separated list of repos to include |
| `--all` | `false` | Include all eligible repos without prompting |
| `--mint-project` | | GCP project for the mint |
| `--mint-region` | `us-central1` | GCP region for the mint |
| `--inference-project` | | Default GCP project for inference |
| `--concurrency` | `8` | Max parallel API calls (capped at 64) |
| `--force` | `false` | Overwrite output file if it already exists |

### Discovery

The command discovers repos by checking:

1. **Per-repo guard variable** (`FULLSEND_PER_REPO_INSTALL`) — identifies per-repo installations
2. **Per-org config enrollment** (`config.yaml` in `.fullsend` repo) — identifies per-org installations
3. **Workflow ref** — extracts the `@ref` from scaffold shim workflow files

### Defaults computation

Default values for `fullsend_ref` and `inference_region` are computed using the mode (most common value) across discovered repos. Per-repo overrides are generated only for fields that differ from defaults.

### Selection modes

For org targets, one of `--all` or `--repos` is required:

- `--all`: include all discovered repos
- `--repos`: include only the specified repos (comma-separated `owner/repo` names)

## `repos install`

Install fullsend on repos defined in a manifest that are not yet installed.

Runs in three phases:

1. **Parallel discovery** — check which repos are already installed via guard variables
2. **Sequential WIF** — provision WIF infrastructure per repo (not concurrent-safe)
3. **Parallel scaffold** — commit scaffold files and write variables/secrets

```bash
fullsend repos install -f repos.yaml
fullsend repos install --dry-run
fullsend repos install acme/api acme/web
fullsend repos install "acme/*" --direct --concurrency 8
```

When repos are specified as positional arguments, only those repos are installed. Glob patterns (e.g. `acme/*`) are matched against manifest entries. When no repos are specified, all manifest repos are installed.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be installed without making changes |
| `--skip-mint-check` | `false` | Skip mint URL discovery and org registration (EnsureOrgInMint). Use when orgs are already registered in the mint. |
| `--concurrency` | `4` | Max parallel operations (1-32) |
| `--roles` | `triage,coder,review,fix,retro,prioritize` | Agent roles to install |
| `--direct` | `false` | Push scaffold directly to default branch (skip PR) |

### Common workflows

Install all repos from a manifest (first run — registers new orgs in the mint):

```bash
fullsend repos install -f repos.yaml
```

Preview changes without modifying infrastructure:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Install specific repos (orgs already registered):

```bash
fullsend repos install acme/api acme/web --skip-mint-check
```

> **Note:** Without `--skip-mint-check`, `repos install` will register any new
> orgs found in the manifest into the mint's `ALLOWED_ORGS`. This modifies
> shared mint infrastructure. Use `--skip-mint-check` when orgs are already
> registered or when you want to skip this step.

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
- **REF** — Current workflow ref (`@v2.3.0`, `@main`, etc.)
- **STATUS** — `installed`, `not installed`, or `error`
- **DRIFT** — Fields that differ from the manifest, or `none`

**JSON output** (`--json`) returns the full `StatusResult` object with per-repo details and aggregate summary counts.

### Exit codes

The command returns a non-zero exit code when any repo has drift, is not installed, or encountered an error. This makes it suitable for CI checks.

### Authentication

Requires a GitHub token via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`.

## `repos diff`

Show configuration drift between the `repos.yaml` manifest and actual forge state. Only examines repos that are already installed (guard variable is `"true"`).

```bash
fullsend repos diff
fullsend repos diff -f path/to/repos.yaml
fullsend repos diff --repo owner/repo1 --repo owner/repo2
fullsend repos diff --json
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |
| `--json` | | `false` | Emit JSON output instead of table |
| `--repo` | | | Filter to specific repos (repeatable) |
| `--concurrency` | | `8` | Max parallel API calls |

### JSON output

With `--json`, returns a `DiffResult` object:

```json
{
  "changes": [
    {"owner": "acme", "repo": "api", "field": "FULLSEND_MINT_URL", "type": "variable", "action": "update", "old_value": "...", "new_value": "..."}
  ],
  "warnings": ["acme/web: not installed (guard variable missing) — run `repos install`"]
}
```

### Exit codes

Returns a non-zero exit code when drift exists (variables differ from manifest or secrets are missing). This makes it suitable for CI drift checks.

## `repos sync`

Reconcile configuration drift for installed repos by writing variables and secrets to match the manifest's desired state. Variables are only written when drift is detected; secrets are always written for convergence since their values cannot be read back.

```bash
fullsend repos sync
fullsend repos sync -f repos.yaml --dry-run
fullsend repos sync --repo acme/api --repo acme/web
fullsend repos sync --json
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |
| `--dry-run` | | `false` | Preview changes without applying them |
| `--json` | | `false` | Emit JSON output instead of table |
| `--repo` | | | Filter to specific repos (repeatable) |
| `--concurrency` | | `4` | Max parallel operations (1-32) |

### JSON output

With `--json`, the output schema depends on mode:

- **`sync --dry-run --json`** — returns a `DiffResult` (same schema as `repos diff --json`)
- **`sync --json`** — returns a `SyncResult`:

```json
{
  "applied": [
    {"owner": "acme", "repo": "api", "field": "FULLSEND_MINT_URL", "type": "variable", "action": "update", "old_value": "...", "new_value": "..."}
  ],
  "failed": 0,
  "warnings": []
}
```

### Scope

Sync reconciles variables (`FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`) and secrets (`FULLSEND_GCP_PROJECT_ID`). It does **not** touch scaffold shim version (`@ref`) or harness files — use `repos upgrade` for those.

## `repos add`

Add one or more repo entries to the `repos.yaml` manifest file, editing it in place. Use `--install` to also install fullsend on the added repos after updating the manifest.

```bash
fullsend repos add acme/new-api acme/new-web
fullsend repos add acme/new-api --install --direct
fullsend repos add acme/new-api --dry-run
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be added without making changes |
| `--install` | `false` | Also install fullsend on the added repos |
| `--concurrency` | `4` | Max parallel operations (1-32, used with `--install`) |
| `--direct` | `false` | Push scaffold directly to default branch (used with `--install`) |
| `--roles` | default roles | Agent roles to install (used with `--install`) |

Duplicate entries are silently skipped. Glob patterns (e.g. `acme/*`) are allowed as manifest entries.

> **Note:** With `--install`, the manifest is updated before installation begins.
> If installation fails for some repos, those entries remain in the manifest as
> desired state. Run `fullsend repos status` to identify repos that need
> re-installation, then `fullsend repos install <repo>` to retry.

## `repos remove`

Remove one or more repo entries from the `repos.yaml` manifest file, editing it in place. When multiple repos are targeted (via globs or explicit bulk lists), the command prompts for confirmation unless `--yes` is set.

Use `--uninstall` to tear down fullsend from the repos before removing them from the manifest (deletes workflow, variables, secrets, and WIF).

```bash
fullsend repos remove acme/old-api
fullsend repos remove "acme/*" --yes
fullsend repos remove acme/old-api --uninstall
fullsend repos remove acme/old-api --uninstall --skip-wif-cleanup
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be removed without making changes |
| `--uninstall` | `false` | Tear down fullsend from repos before removing from manifest |
| `--yes` | `false` | Skip confirmation prompt when multiple repos are targeted |
| `--skip-wif-cleanup` | `false` | Skip GCP WIF provider deletion (only with `--uninstall`) |
| `--concurrency` | `4` | Max parallel operations (1-32, used with `--uninstall`) |

## `repos uninstall`

Tear down fullsend from the specified repos by deleting workflow files, variables, secrets, and WIF infrastructure. Does **not** modify `repos.yaml` — use `repos remove` for that.

When multiple repos are targeted (via globs or explicit bulk lists), the command prompts for confirmation unless `--yes` is set.

Runs in two phases:
1. **Parallel per-repo cleanup** — delete workflow, variables, secrets (concurrent)
2. **Sequential WIF deregistration** — deregister from mint and delete WIF provider

```bash
fullsend repos uninstall acme/old-api
fullsend repos uninstall "acme/*" --yes
fullsend repos uninstall acme/old-api --skip-wif-cleanup
fullsend repos uninstall acme/old-api --dry-run
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be uninstalled without making changes |
| `--yes` | `false` | Skip confirmation prompt when multiple repos are targeted |
| `--skip-wif-cleanup` | `false` | Skip GCP WIF provider deletion |
| `--concurrency` | `4` | Max parallel operations (1-32) |

## `repos upgrade`

Upgrades the fullsend scaffold workflow ref for repos defined in a `repos.yaml` manifest. Reads each repo's current workflow file, compares against the manifest's `fullsend_ref` (or `--ref` override), and commits the updated workflow.

Floating refs (`latest`, `main`, `v0`) are skipped. Downgrades are blocked unless `--force` is set.

```bash
fullsend repos upgrade -f repos.yaml
fullsend repos upgrade --dry-run
fullsend repos upgrade acme/api acme/web
fullsend repos upgrade --ref v2.4.0
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |
| `--ref` | | | Override manifest `fullsend_ref` for all repos |
| `--dry-run` | | `false` | Preview what would be upgraded without making changes |
| `--force` | | `false` | Upgrade even if current ref is newer than target |
| `--concurrency` | | `4` | Max parallel operations (1-32) |
| `--direct` | | `false` | Push scaffold directly to default branch (skip PR) |

### Positional arguments

When repos are specified as positional arguments, only those repos are upgraded. Supports exact `owner/repo` names and glob patterns (e.g. `acme/*`), matched via `filepath.Match` against manifest entries. When no repos are specified, all manifest repos are upgraded.

### Limitations

**Tag-only pinning.** `repos upgrade` writes the manifest's `fullsend_ref` tag directly into `uses:` lines. If a repo currently uses SHA pinning (e.g. `@abc123 # v1.9.0`), the upgrade replaces the SHA with the tag, losing the pin. To restore SHA pinning after upgrade, re-run a per-repo install or manually resolve the tag to a SHA. A future enhancement will resolve tags to SHAs via the forge API.

## `repos upgrade-mint`

Verifies the token mint Cloud Function matches the manifest configuration. Discovers the current mint deployment and checks that its URL matches the manifest's `mint.url`.

Run this before `repos upgrade` to ensure mint compatibility.

```bash
fullsend repos upgrade-mint -f repos.yaml
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |

## See also

- [Getting Started](../guides/getting-started/) — Standard per-repo installation
- [Operations](../guides/getting-started/operations.md) — Day-2 administration
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
