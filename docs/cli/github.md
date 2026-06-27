---
sidebar_label: fullsend github
---

# fullsend github

Configure fullsend on GitHub organizations and repositories without requiring GCP credentials. All GCP infrastructure values (mint URL, WIF provider) are passed as flags.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend github setup <org\|owner/repo>` | Configure fullsend for an org or repo |
| `fullsend github enroll <org> [repo...]` | Enable repositories for agent workflows |
| `fullsend github unenroll <org> [repo...]` | Disable repositories from agent workflows |
| `fullsend github set <target> <key> <value>` | Update a single config value (secret or variable) |
| `fullsend github status <org>` | Analyze GitHub-side installation state |
| `fullsend github sync-scaffold <org>` | Update workflow templates to current CLI version |
| `fullsend github uninstall <org>` | Remove fullsend GitHub configuration |

## `github setup`

Configures a GitHub organization or repository with fullsend. Creates the `.fullsend` config repo (per-org mode), installs GitHub Apps, and sets variables and secrets.

**Per-org mode** requires GitHub organization owner access:

```bash
fullsend github setup <org> \
  --mint-url="<MINT_URL>" \
  --inference-project "<GCP_PROJECT>" \
  --inference-wif-provider "<WIF_PROVIDER>"
```

**Per-repo mode** requires repo admin access only:

```bash
fullsend github setup <owner/repo> \
  --mint-url="<MINT_URL>" \
  --inference-project "<GCP_PROJECT>" \
  --inference-wif-provider "<WIF_PROVIDER>"
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mint-url` | | HTTPS endpoint of the token mint service |
| `--inference-project` | | GCP project ID for Agent Platform |
| `--inference-wif-provider` | | Full WIF provider resource name |
| `--inference-region` | `global` | GCP region for inference |
| `--skip-app-setup` | `false` | Skip GitHub App creation/installation |
| `--app-set` | `fullsend-ai` | App set name prefix for GitHub Apps |
| `--agents` | `fullsend,triage,coder,review,retro,prioritize` | Agent roles to provision |
| `--direct` | `false` | Push scaffold directly instead of creating a PR |

### Required OAuth scopes

| Scope | Per-org | Per-repo |
|-------|:-------:|:--------:|
| `repo` | x | x |
| `workflow` | x | x |
| `admin:org` | x | |

## `github enroll`

Enables agent workflows on repositories by updating `config.yaml` in the `.fullsend` repo and triggering enrollment PRs.

```bash
fullsend github enroll <org> <repo-name> [repo-name...]
fullsend github enroll <org> --all
```

## `github unenroll`

Disables agent workflows on repositories.

```bash
fullsend github unenroll <org> <repo-name> [repo-name...]
fullsend github unenroll <org> --all [--yolo]
```

The `--all` flag prompts for confirmation. Pass `--yolo` to skip the prompt.

## `github set`

Updates a single configuration value (secret or variable) on a GitHub org or repo.

```bash
fullsend github set <org|owner/repo> <key> <value>
```

## `github status`

Analyzes the GitHub-side installation state. Read-only.

```bash
fullsend github status <org>
```

## `github sync-scaffold`

Updates workflow templates in enrolled repositories to match the current CLI version.

```bash
fullsend github sync-scaffold <org>
```

## `github uninstall`

Removes fullsend GitHub configuration for an organization. Deletes the `.fullsend` config repo and associated resources.

```bash
fullsend github uninstall <org> [--yolo] [--app-set <name>]
```

## See also

- [Setting up with pre-provisioned infrastructure](../reference/github-setup.md) — full setup guide for GitHub maintainers
- [Configuring GitHub for fullsend](../guides/getting-started/configuring-github.md) — getting started guide
- [Installation guide](../reference/installation.md) — all-in-one `admin install`
