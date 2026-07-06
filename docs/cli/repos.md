---
sidebar_label: fullsend repos
---

# fullsend repos

Manage per-repo installations across multiple orgs via a declarative `repos.yaml` manifest. Compare the manifest's desired state against actual forge state and report installation status and configuration drift.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend repos status` | Compare manifest against actual repo state |

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
