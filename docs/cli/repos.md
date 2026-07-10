---
sidebar_label: fullsend repos
---

# fullsend repos

Manage per-repo installations across multiple orgs via a declarative `repos.yaml` manifest. Compare the manifest's desired state against actual forge state and report installation status and configuration drift.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend repos init <org\|owner/repo>` | Generate a repos.yaml manifest by discovering existing installations |
| `fullsend repos install` | Install fullsend on uninstalled manifest repos |
| `fullsend repos status` | Compare manifest against actual repo state |

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
fullsend repos install --repo acme/api --repo acme/web
fullsend repos install --direct --concurrency 8
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be installed without making changes |
| `--repo` | (all) | Install specific repos only (repeatable) |
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
fullsend repos install --repo acme/api --repo acme/web --skip-mint-check
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
fullsend repos status --repo owner/repo1 --repo owner/repo2
fullsend repos status --json
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |
| `--json` | | `false` | Emit JSON output instead of table |
| `--repo` | | | Filter to specific repos (repeatable) |
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

## See also

- [Getting Started](../guides/getting-started/) — Standard per-repo installation
- [Operations](../guides/getting-started/operations.md) — Day-2 administration
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
