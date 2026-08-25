---
sidebar_label: fullsend agent
---

# fullsend agent

Manage agent registrations in fullsend config. Add, list, set (runtime, model, effort), update, and remove agents.

`agent add` and `agent update` fetch remote content and resolve GitHub URLs. Authentication is via `gh` CLI or `GH_TOKEN` environment variable.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend agent add <url-or-path>` | Register an agent in config |
| `fullsend agent list` | List registered agents |
| `fullsend agent update <name> [sha]` | Update a URL agent to a new commit SHA |
| `fullsend agent set <name>` | Set an agent's runtime, model or effort |
| `fullsend agent remove <name>` | Remove an agent from config |

## `agent add`

Register an agent in config by URL or local path. URL sources are automatically pinned to a specific commit SHA and annotated with a `#sha256=...` integrity hash. The URL prefix is added to `allowed_remote_resources` if not already present.

```bash
fullsend agent add https://github.com/my-org/agents/blob/main/harness/lint.yaml --fullsend-dir .fullsend
fullsend agent add harness/custom-review.yaml --name my-review --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |
| `--name` | derived from filename | Explicit agent name |

GitHub blob URLs are resolved to pinned `raw.githubusercontent.com` URLs. Non-GitHub URLs must already contain a commit SHA in the path. Local paths must be relative, must not contain path traversal (`..`), and the file must exist. If an agent with the same name already exists, the command fails.

## `agent list`

List all agents registered in config, showing each agent's name and source.

```bash
fullsend agent list --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |

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
fullsend agent update triage --fullsend-dir .fullsend
fullsend agent update triage a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |

Only URL agents can be updated — local path agents have nothing to pin. Non-GitHub URL agents require an explicit SHA argument. The integrity hash is recomputed by fetching the content at the new SHA.

## `agent set`

Sets `runtime`, `model` and/or `effort` for one agent in `.fullsend/config.yaml` (per-repo
configs). A built-in agent (`triage`, `code`, `review`, `fix`, `retro`, `prioritize`) without an
entry gets a name-only entry; a custom agent's settings land on its `source:` entry (or, for an
agent registered in `config.base.yaml`, on a name-only overlay entry that merges onto it). Only the
flags given change; pass an empty value (`--model ""`) to clear a setting. The result is validated
before it is written.

```bash
fullsend agent set code --fullsend-dir .fullsend --runtime claude --model sonnet --effort high
fullsend agent set triage --fullsend-dir .fullsend --model xai-vertex/xai/grok-4.6
```

### Flags

| Flag | Description |
|------|-------------|
| `--fullsend-dir` | Path to the `.fullsend` configuration directory (required) |
| `--runtime` | Agent runtime for this agent (`claude` or `pi`) |
| `--model` | Model for this agent — an alias, a model id, or `provider/id` on pi |
| `--effort` | Effort level for this agent (`low`, `medium`, `high`, `xhigh`, `max`) |

See [Runtimes — per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort) for precedence.

## `agent remove`

Remove an agent from config. If the removed agent was the last one using a given `allowed_remote_resources` prefix, that prefix is also cleaned up.

```bash
fullsend agent remove triage --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — building custom agents and configuring existing ones
- [Default, derived, and custom agents](../agents/topics/default-vs-custom.md) — terminology and classification
- [Configuring with skills](../guides/user/customizing-with-skills.md) — extending agents with skills
