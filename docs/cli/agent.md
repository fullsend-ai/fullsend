---
sidebar_label: fullsend agent
---

# fullsend agent

Manage agents in fullsend config. Generate a new agent, add, list, set (runtime, model, effort), update, and remove agents.

`agent add` and `agent update` fetch remote content and resolve GitHub URLs. Authentication is via `gh` CLI or `GH_TOKEN` environment variable.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend agent new <name>` | Generate a complete custom agent and register it |
| `fullsend agent add <url-or-path>` | Register an agent in config |
| `fullsend agent list` | List registered agents |
| `fullsend agent update <name> [sha]` | Update a URL agent to a new commit SHA |
| `fullsend agent set <name>` | Set an agent's runtime, model or effort |
| `fullsend agent remove <name>` | Remove an agent from config |

## `agent new`

Generate a complete, valid, runnable custom agent and register it. This is the
fastest path from nothing to a working agent — you edit prose, not plumbing.

```bash
fullsend agent new lint-docs --fullsend-dir .fullsend \
  --role triage --description "Check docs changes for broken links"
```

```
  ✓ Created agent "lint-docs" in .fullsend
  harness/lint-docs.yaml
  agents/lint-docs.md
  schemas/lint-docs-result.schema.json
  scripts/post-lint-docs.sh
  policies/base.yaml
  providers/vertex-ai.yaml
  providers/github-ro.yaml
  profiles/fullsend-vertex-ai.yaml
  profiles/fullsend-github-ro.yaml
  ✓ Added agent "lint-docs"

Next:
  1. Fill in the marked sections of agents/lint-docs.md — that file is the agent's prompt.
  2. Test locally:
       fullsend run lint-docs --fullsend-dir .fullsend \
         --target-repo . --env-file .env.local
     .env.local needs GITHUB_ISSUE_URL, ANTHROPIC_VERTEX_PROJECT_ID, CLOUD_ML_REGION
     and GH_TOKEN. See docs/guides/user/running-agents-locally.md.
  3. Commit .fullsend, then comment `/fs-lint-docs` on an issue or pull request to run it in CI.
```

The generated tree:

```bash
find .fullsend -type f | sort
```

```
.fullsend/agents/lint-docs.md
.fullsend/config.yaml
.fullsend/harness/lint-docs.yaml
.fullsend/policies/base.yaml
.fullsend/profiles/fullsend-github-ro.yaml
.fullsend/profiles/fullsend-vertex-ai.yaml
.fullsend/providers/github-ro.yaml
.fullsend/providers/vertex-ai.yaml
.fullsend/schemas/lint-docs-result.schema.json
.fullsend/scripts/post-lint-docs.sh
```

Only `agents/lint-docs.md` needs your attention — it is the agent's prompt and
it ships with marked sections to fill in. Everything else is complete.

### What gets written

| File | Written | Overwritten by `--force` |
|------|---------|--------------------------|
| `harness/<name>.yaml` | always | yes |
| `agents/<name>.md` | always | yes |
| `schemas/<name>-result.schema.json` | always | yes |
| `scripts/post-<name>.sh` (mode 0755) | always | yes |
| `policies/base.yaml` | when absent | **no** |
| `providers/*.yaml` (per role) | when absent | **no** |
| `profiles/*.yaml` (per role) | when absent | **no** |
| `scripts/validate-output-schema.sh` | with `--validation-loop`, when absent | **no** |
| `config.yaml` `agents:` entry | unless `--no-register` | n/a |

The policy, provider and profile files are shared by every agent in the
directory, so they are never overwritten — including with `--force`. A per-repo
install does not vendor them, which is why `agent new` writes them.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |
| `-f`, `--file` | | Read the agent definition from a spec YAML file |
| `--role` | `triage` | Mint role the agent runs as (see the table below) |
| `--description` | `Custom <name> agent.` | One-line description; written to both the harness and the agent definition |
| `--on` | `command:/fs-<name>` | Trigger preset; mutually exclusive with `--trigger` |
| `--trigger` | | Raw CEL trigger expression; mutually exclusive with `--on` |
| `--model` | `opus` | Model for the agent |
| `--effort` | `high` | Effort level (`low`, `medium`, `high`, `xhigh`, `max`) |
| `--runtime` | | Agent runtime recorded in `config.yaml` (`claude`, `pi` or `codex`) |
| `--slug` | `<owner>-<name>` | Harness slug; `<owner>` comes from the `origin` remote |
| `--image` | per-role pin | Sandbox image |
| `--timeout-minutes` | `15` | Agent timeout in minutes |
| `--validation-loop` | `false` | Add a `validation_loop` checking output against the schema |
| `--no-register` | `false` | Write the files but do not touch `config.yaml` |
| `--force` | `false` | Overwrite generated files (never shared assets) |
| `--dry-run` | `false` | Validate and print what would be written, writing nothing |

### Roles

`--role` is the **mint** role, not the agent's name. It selects which GitHub App
identity and permissions the mint issues. The hosted mint serves these:

| `--role` | Permissions | Providers |
|----------|-------------|-----------|
| `triage` (default) | `contents:read`, `issues:write`, `metadata:read` | vertex-ai, github-ro |
| `review` | `contents:read`, `pull_requests:write`, `issues:write`, `checks:read`, `metadata:read` | vertex-ai, github-ro |
| `coder` | `contents:write`, `packages:read`, `pull_requests:write`, `issues:write`, `checks:read`, `metadata:read` | vertex-ai, github |
| `retro` | `actions:read`, `contents:read`, `pull_requests:write`, `issues:write`, `metadata:read` | vertex-ai, github-ro, github-artifacts |
| `prioritize` | `contents:read`, `issues:write`, `organization_projects:write`, `metadata:read` | vertex-ai, github-ro |

Pick the role whose permissions fit what the agent does. An unknown role fails
immediately with this table rather than returning `403` from the mint at the
first dispatch. To use a role the hosted mint does not serve, you need your own
mint — see [Custom Agent Identity](../guides/user/custom-agent-identity.md).

### Triggers

Every generated agent gets a `trigger:`. An agent without one registers and
validates but is **silently never dispatched**, so `agent new` refuses to write
one. `--on` takes a preset:

| `--on` | Fires when |
|--------|-----------|
| `command:/<command>` (default `/fs-<name>`) | Someone comments the slash command on an issue, or on a pull request that is not from a fork |
| `label:<label>` (default `<name>`) | The label is added |
| `issue-opened` | A new issue is opened |
| `pr-opened` | A non-fork pull request is opened, updated, or marked ready |

The emitted expressions are exactly the ones in
[CEL Triggers Reference](../guides/user/cel-triggers-reference.md#common-trigger-patterns);
a test asserts the two stay identical. For anything else, pass `--trigger` with
raw CEL — it is compiled before any file is written.

Both `command:` and `pr-opened` refuse comments and pull requests from forks.
That matters most for `--role coder`, which can write to the repository.

### Spec files

`-f` reads the same settings from a YAML document, so a local coding agent can
produce one:

```yaml
version: "1"
name: link-check
role: review
description: Check that links in changed docs resolve
on: label:needs-link-check
model: opus
timeout_minutes: 20
```

```bash
fullsend agent new -f link-check.agent.yaml --fullsend-dir .fullsend
```

```
  ✓ Created agent "link-check" in .fullsend
  harness/link-check.yaml
  agents/link-check.md
  schemas/link-check-result.schema.json
  scripts/post-link-check.sh
  policies/base.yaml  (already present, left unchanged)
  providers/vertex-ai.yaml  (already present, left unchanged)
  providers/github-ro.yaml  (already present, left unchanged)
  profiles/fullsend-vertex-ai.yaml  (already present, left unchanged)
  profiles/fullsend-github-ro.yaml  (already present, left unchanged)
  ✓ Added agent "link-check"
```

Unknown keys are rejected rather than ignored, so a typo does not silently
produce a different agent. Command-line flags override spec keys.

### Checking the result

`agent new` validates what it generates before it writes anything. To re-check
later — after you have edited the harness by hand, for example — load it with
the same loader dispatch uses:

```bash
fullsend lock lint-docs --fullsend-dir .fullsend --offline
```

```
⚡ fullsend dev
  Autonomous agentic development for Git-hosted organizations
→ Locking dependencies: lint-docs

  ✓ Harness has no remote dependencies — nothing to lock
```

`--offline` proves the agent needs no network. Note that `fullsend agent list`
shows **registrations** and does not open harness files, so it is not a
validity check:

```bash
fullsend agent list --fullsend-dir .fullsend
```

```
NAME       SOURCE
lint-docs  harness/lint-docs.yaml
```

Per-agent overrides compose on top of the generated harness:

```bash
fullsend agent set lint-docs --fullsend-dir .fullsend --model sonnet
```

```
  ✓ Set agent "lint-docs": runtime="" model="sonnet" effort="" (empty = inherit)
```

To see what would be generated without writing anything, use `--dry-run`. It
prints the file list and every rendered body:

```bash
fullsend agent new report --fullsend-dir .fullsend --dry-run
```

```
    Dry run: would create agent "report" in .fullsend
  harness/report.yaml
  agents/report.md
  schemas/report-result.schema.json
  scripts/post-report.sh
  policies/base.yaml  (already present, would be left unchanged)
  ...
    Nothing was written and no agent was registered
```

And to undo a generated registration:

```bash
fullsend agent remove link-check --fullsend-dir .fullsend
```

```
  ✓ Removed agent "link-check"
```

`agent remove` unregisters the agent; the generated files stay on disk for you
to delete or keep.

### Running it

Generation is step one. To actually run the agent, fill in the marked sections
of `agents/<name>.md`, then follow
[Running agents locally](../guides/user/running-agents-locally.md) — a real run
needs GCP credentials, a sandbox image, and an `--env-file` supplying
`GITHUB_ISSUE_URL`, `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION` and
`GH_TOKEN`. Set `POST_<NAME>_DRY_RUN=1` (for example
`POST_LINT_DOCS_DRY_RUN=1`) so the generated post-script prints its comment
instead of posting it.

In CI, commit `.fullsend/` and fire the agent with whatever its trigger
describes — for the default preset, comment `/fs-<name>` on an issue or pull
request.

### Troubleshooting

| Error | Cause | Fix |
|-------|-------|-----|
| `unknown role "scribe"` followed by the role table | The role is not one the hosted mint serves | Use one of the five listed; for a custom role see [Custom Agent Identity](../guides/user/custom-agent-identity.md) |
| `agent name "..." contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)` | The name would not be safe to interpolate into a shell script | Rename. Nothing is written when this fires |
| `these files already exist:` followed by a list | An agent of that name was already generated | Pick another name, or pass `--force`. `--force` never overwrites `policies/`, `providers/` or `profiles/` |
| `agent "..." already exists in config` | The name is registered in `config.yaml` | `fullsend agent remove <name>` first. `--force` deliberately does not override this |
| `trigger does not compile: ERROR: <input>:1:5: Syntax error: ...` | A `--trigger` expression is not valid CEL, or does not return a boolean | Compare against the `--on` presets above |
| `unknown --on preset "..."` followed by the preset list | `--on` is not one of the four presets | Use a listed preset, or pass raw CEL with `--trigger` |
| `a trigger is required: pass --on with a preset, or --trigger` | `--trigger ""` was passed explicitly | Give a real trigger. A trigger-less agent is silently never dispatched |
| `fullsend dir ... does not exist; run ` + "`fullsend github setup`" + ` first` | `--fullsend-dir` points at nothing | Scaffold the repo first |
| `validating files: policy: stat .../policies/base.yaml: no such file or directory` | A hand-edited harness references a file that is not there | Re-run `agent new`, which writes the policy when absent |
| Agent crashes at 0s in CI | The sandbox cannot reach Vertex — a provider or profile file is missing | Confirm `providers/` and `profiles/` exist next to the harness |
| `runner env ... is not set` at `fullsend run` | A `${VAR}` in the harness `env` block is unset | `agent new` does not check host variables at generation time; supply them via `--env-file` locally or the workflow `env:` block in CI |

## `agent add`

Register an agent in config by URL or local path. URL sources are automatically pinned to a specific commit SHA and annotated with a `#sha256=...` integrity hash. When a URL references a branch or tag (rather than a commit SHA), the original ref is stored in the config entry's `ref` field so that subsequent `agent update` calls re-resolve against the same branch. The URL prefix is added to `allowed_remote_resources` if not already present.

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

Update a URL-based agent to a new commit SHA and recompute the `#sha256=...` integrity hash. If no SHA is provided, the branch ref stored at adoption time is re-resolved; if no ref was stored (backward-compatible entries), the default branch HEAD is used.

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
| `--runtime` | Agent runtime for this agent (`claude`, `pi` or `codex`) |
| `--model` | Model for this agent — an alias, a model id, or `provider/id` on pi and codex (codex takes OpenAI ids only) |
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
