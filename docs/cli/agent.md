---
sidebar_label: fullsend agent
---

# fullsend agent

Manage agent registrations in fullsend config. Add, list, update, and remove agents, or migrate legacy `customized/` overrides to config-driven agents.

`agent add`, `agent update`, and `agent migrate-customizations` (non-dry-run) fetch remote content and resolve GitHub URLs. Authentication is via `gh` CLI or `GH_TOKEN` environment variable.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend agent add <url-or-path>` | Register an agent in config |
| `fullsend agent list` | List registered agents |
| `fullsend agent update <name> [sha]` | Update a URL agent to a new commit SHA |
| `fullsend agent remove <name>` | Remove an agent from config |
| `fullsend agent migrate-customizations` | Migrate `customized/` overrides to config-driven agents |

## `agent add`

Register an agent in config by URL or local path. URL sources are automatically pinned to a specific commit SHA and annotated with a `#sha256=...` integrity hash. The URL prefix is added to `allowed_remote_resources` if not already present.

```bash
fullsend agent add https://github.com/my-org/agents/blob/main/harness/lint.yaml --agent-dir .fullsend
fullsend agent add harness/custom-review.yaml --name my-review --agent-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agent-dir` | | Base directory containing agent definitions (required) |
| `--name` | derived from filename | Explicit agent name |

GitHub blob URLs are resolved to pinned `raw.githubusercontent.com` URLs. Non-GitHub URLs must already contain a commit SHA in the path. Local paths must be relative, must not contain path traversal (`..`), and the file must exist. If an agent with the same name already exists, the command fails.

## `agent list`

List all agents registered in config, showing each agent's name and source.

```bash
fullsend agent list --agent-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agent-dir` | | Base directory containing agent definitions (required) |

Read-only. Displays a table with `NAME` and `SOURCE` columns. For URL agents, the `#sha256=...` integrity hash suffix is stripped from the displayed source for readability. Disabled agents (`enabled: false`) are included in the listing.

Example output:
```
NAME     SOURCE
triage   https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml
my-lint  harness/my-lint.yaml
```

## `agent update`

Update a URL-based agent to a new commit SHA and recompute the `#sha256=...` integrity hash. If no SHA is provided, the default branch HEAD is resolved automatically.

```bash
fullsend agent update triage --agent-dir .fullsend
fullsend agent update triage a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 --agent-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agent-dir` | | Base directory containing agent definitions (required) |

Only URL agents can be updated — local path agents have nothing to pin. Non-GitHub URL agents require an explicit SHA argument. The integrity hash is recomputed by fetching the content at the new SHA.

## `agent remove`

Remove an agent from config. If the removed agent was the last one using a given `allowed_remote_resources` prefix, that prefix is also cleaned up.

```bash
fullsend agent remove triage --agent-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agent-dir` | | Base directory containing agent definitions (required) |

## `agent migrate-customizations`

Scan the `customized/` directory and migrate each override to a config-driven agent. Changes are committed to a branch and delivered via pull request. Use `--dry-run` to preview changes without creating a PR.

```bash
fullsend agent migrate-customizations --agent-dir .fullsend --dry-run
fullsend agent migrate-customizations --agent-dir .fullsend --repo owner/repo
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--agent-dir` | | Base directory containing agent definitions (required) |
| `--repo` | | Target repository (`owner/repo`) for the migration PR (required unless `--dry-run`) |
| `--dry-run` | `false` | Show what would change without creating a PR |

### Override classification

The tool classifies each override and takes the appropriate action:

| Override type | Detection | Action |
|---------------|-----------|--------|
| Dead | Agent already registered in config | Delete `customized/` files |
| Custom | Not in upstream scaffold | Move files to regular directories, register local path in config |
| Modified | Standard scaffold agent, not yet in config | Generate a `base:` composition harness with the minimal diff, register in config |

For modified agents, the migration produces a thin `base:` harness containing only the fields that differ from upstream.

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — building custom agents and configuring existing ones
- [Default, derived, and custom agents](../agents/topics/default-vs-custom.md) — terminology and classification
- [Customizing with skills](../guides/user/customizing-with-skills.md) — extending agents with skills
