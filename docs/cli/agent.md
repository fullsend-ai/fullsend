---
sidebar_label: fullsend agent
---

# fullsend agent

Manage agents in fullsend config. Generate a new agent, add, list, set (runtime, model, effort), update, and remove agents.

`agent add` and `agent update` fetch remote content and resolve GitHub URLs. Authentication is via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`.

Every subcommand uses `.fullsend` in the current directory, so run them from
the repository root. Pass `--fullsend-dir <path>` to use another directory.
When `--fullsend-dir` is omitted and `.fullsend` does not exist, the command
stops with `no .fullsend directory in the current directory; run from the
repository root or pass --fullsend-dir <path>`.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend agent new <name>` | Generate a complete custom agent and register it |
| `fullsend agent add <url-or-path>` | Register an agent in config |
| `fullsend agent list` | List registered agents |
| `fullsend agent update <name> [sha]` | Update a URL agent or a local harness `base:` URL to a new commit SHA |
| `fullsend agent set <name>` | Set an agent's runtime, model or effort |
| `fullsend agent remove <name>` | Remove an agent from config |

## `agent new`

Generate a complete, valid, runnable custom agent and register it. Every file
an agent needs is written for you; the only one you have to edit is the
instructions the agent follows.

```bash
fullsend agent new lint-docs \
  --role triage --description "Check docs changes for broken links"
```

```
  ✓ Created agent "lint-docs" in .fullsend
  harness/lint-docs.yaml
  agents/lint-docs.md
  schemas/lint-docs-result.schema.json
  scripts/post-lint-docs.sh
  policies/base.yaml
  ✓ Added agent "lint-docs"

Next:
  1. Fill in the marked sections of agents/lint-docs.md — that file is the agent's prompt.
  2. Test locally, printing the result instead of commenting:
       POST_LINT_DOCS_DRY_RUN=1 fullsend run lint-docs \
         --target-repo . --env-file .env.local
     .env.local needs GITHUB_ISSUE_URL, ISSUE_NUMBER, REPO_FULL_NAME,
     GH_TOKEN, ANTHROPIC_VERTEX_PROJECT_ID, CLOUD_ML_REGION, and
     GOOGLE_APPLICATION_CREDENTIALS pointing at a GCP credentials file;
     the run stops before it starts without them.
     GH_TOKEN must be a real token: a connectivity check runs before the
     agent does. See docs/guides/user/running-agents-locally.md.
  3. Commit .fullsend, then comment `/fs-lint-docs` on an issue or pull request to run it in CI.
```

Step 2 lists the Vertex route's variables. For the full walkthrough — picking
a route (Claude, codex, or pi; Vertex or OpenAI), what a run does end to end,
and how to try it without a model — see
[Bring Your Own Agent](../guides/user/bring-your-own-agent.md).

The generated tree:

```bash
find .fullsend -type f | sort
```

```
.fullsend/agents/lint-docs.md
.fullsend/config.yaml
.fullsend/harness/lint-docs.yaml
.fullsend/policies/base.yaml
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
| `scripts/validate-output-schema.sh` | with `--validation-loop`, when absent | **no** |
| `config.yaml` `agents:` entry | unless `--no-register` | n/a |

The generated harness's `providers:` entries are bare names (`vertex-ai`,
`github-ro`, ...), not paths, and no `providers/` or `profiles/` files are
written. `fullsend run` resolves each built-in name to the provider
definition and profile in the `fullsend` binary, so a fix to one reaches
you with the next `fullsend` release. `policies/base.yaml` is the one shared
file: there is no built-in policy, so `agent new` writes one copy for every
agent in the directory and never overwrites it (not even with `--force`).
Commit it with the agent.

| What | In CI and locally | To customize |
|------|-------------------|--------------|
| `policies/` | Your committed copy is used as-is | Edit `policies/base.yaml`, or add another policy file and point the harness `policy:` at it |
| Built-in providers and their profiles | Resolved from the `fullsend` binary | Copy the provider and profile under your own name (for example `providers/myorg-github-ro.yaml` with `name: myorg-github-ro` and `type: myorg-github-ro`, and `profiles/myorg-github-ro.yaml` with `id: myorg-github-ro`), list the profile under `openshell.profiles`, and declare `myorg-github-ro` instead of the built-in name |

The built-in names (`vertex-ai`, `github`, `github-ro`, `github-artifacts`,
`gitleaks`, `package-registries`, `atlassian-cloud`, `openai`) and their
`fullsend-<name>` profile ids are reserved. A harness that still uses its own
copy under one of them keeps working for now: `fullsend run` uses the copy
and prints a warning naming the migration below. A later release rejects
it. `fullsend-openai` is already rejected.

Bare names need the same `fullsend` release in CI as the one that
generated the agent, or a newer one. The scaffolded workflows run the
binary that matches the workflow commit, so this holds unless your workflow
passes an older `fullsend_version`.

Migrating a harness generated by v0.44.0 or earlier (`providers/*.yaml` and
`profiles/fullsend-*.yaml` referenced by path)? See
[Upgrading agents generated before built-in providers](../guides/user/bring-your-own-agent.md#upgrading-agents-generated-before-built-in-providers)
for the four-step migration to the bare built-in names above.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the `.fullsend` configuration directory |
| `-f`, `--file` | | Read the agent definition from a spec YAML file |
| `--role` | `triage` | Mint role the agent runs as (see the table below) |
| `--description` | `Custom <name> agent.` | One-line description; written to both the harness and the agent definition |
| `--on` | `command:/fs-<name>` | Trigger preset; mutually exclusive with `--trigger` |
| `--trigger` | | A trigger written by hand, in CEL (the expression language dispatch evaluates); mutually exclusive with `--on` |
| `--model` | `opus` | Model for the agent. With `--runtime codex` there is no default: pass an OpenAI id such as `openai/<id>`, or the command refuses |
| `--effort` | `high` | Effort level (`low`, `medium`, `high`, `xhigh`, `max`) |
| `--runtime` | | Agent runtime recorded in `config.yaml` (`claude`, `pi` or `codex`); when omitted, the repo's `runtime:` default applies. The runtime and model decide which credentials the harness asks for: see [Picking a route](#picking-a-route) |
| `--slug` | `<owner>-<name>` | Names the GitHub App to look for when the agent is installed; `<owner>` comes from the `origin` remote |
| `--image` | per-role pin | Container image the agent runs inside |
| `--timeout-minutes` | `15` | Agent timeout in minutes |
| `--validation-loop` | `false` | Add a `validation_loop` checking output against the schema |
| `--no-register` | `false` | Write the files but do not touch `config.yaml` |
| `--force` | `false` | Overwrite generated files (never shared assets) |
| `--dry-run` | `false` | Validate and print what would be written, writing nothing |

### Roles

`--role` is not the agent's name. It decides which GitHub identity the agent
acts as and what that identity may do.

Agents do not carry long-lived credentials. At run time they ask a service
called the **mint** for a short-lived GitHub token, and `role:` is what they
ask for. The mint only issues tokens for roles it knows, so a role it does not
serve fails at the first run rather than at generation time — which is why this
command refuses an unknown one up front. The hosted mint serves these:

| `--role` | Permissions | Providers |
|----------|-------------|-----------|
| `triage` (default) | `contents:read`, `issues:write`, `metadata:read` | vertex-ai, github-ro, openai |
| `review` | `contents:read`, `pull_requests:write`, `issues:write`, `checks:read`, `metadata:read` | vertex-ai, github-ro, openai |
| `coder` | `contents:write`, `packages:read`, `pull_requests:write`, `issues:write`, `checks:read`, `metadata:read` | vertex-ai, github, openai |
| `retro` | `actions:read`, `contents:read`, `pull_requests:write`, `issues:write`, `metadata:read` | vertex-ai, github-ro, github-artifacts, openai |
| `prioritize` | `contents:read`, `issues:write`, `organization_projects:write`, `metadata:read` | vertex-ai, github-ro, openai |

Every provider is a bare name: the runner resolves its definition and
profile from the binary, and nothing is written under `.fullsend/providers/`
or `.fullsend/profiles/`. A codex agent declares no Vertex provider; a pi
agent on an `openai/` model declares none but keeps a commented-out overlay
that adds it (see [Picking a route](#picking-a-route)).

A `review` agent also gets `readonly_repo: true`: the checked-out repository is made read-only in the sandbox, so a reviewer cannot modify the code it reviews. That matches `harness/review.yaml` in fullsend-ai/agents.

Pick the role whose permissions fit what the agent does. An unknown role fails
immediately with this table, rather than returning `403` from the mint the
first time the agent runs. To use a role the hosted mint does not serve, you
need to run your own — see
[Custom Agent Identity](../guides/user/custom-agent-identity.md).

### Picking a route

`--runtime` and `--model` together decide which credentials the generated
harness asks for — Claude on Vertex, codex or pi on OpenAI, or a pi agent
that mixes both. See
[Bring Your Own Agent § Pick a route](../guides/user/bring-your-own-agent.md#pick-a-route)
for the full table and the two mixed-route cases (an OpenAI parent with
Vertex sub-agents, and a Vertex parent with a persona routed to OpenAI).

### Triggers

A trigger is the rule that decides which GitHub events start the agent —
a comment, a label, a new issue, a pull request. Every generated agent gets
one, because an agent without a trigger is accepted everywhere and then simply
never runs, with nothing reported anywhere to tell you why. `agent new`
therefore refuses to write one without a trigger. `--on` takes a preset:

| `--on` | Fires when |
|--------|-----------|
| `command:/<command>` (default `/fs-<name>`) | Someone comments the slash command on an issue, or on a pull request that is not from a fork |
| `label:<label>` (default `<name>`) | The label is added |
| `issue-opened` | A new issue is opened |
| `pr-opened` | A non-fork pull request is opened, updated, or marked ready |

The expressions these presets produce are exactly the ones written out in the
[CEL Triggers Reference](../guides/user/cel-triggers-reference.md#common-trigger-patterns),
and a test keeps the two identical. For anything the presets do not cover, pass
`--trigger` with your own expression — it is compiled and checked before any
file is written, so a mistake fails here rather than at the first event.

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
fullsend agent new -f link-check.agent.yaml
```

```
  ✓ Created agent "link-check" in .fullsend
  harness/link-check.yaml
  agents/link-check.md
  schemas/link-check-result.schema.json
  scripts/post-link-check.sh
  policies/base.yaml  (already present, left unchanged)
  ✓ Added agent "link-check"
```

Unknown keys are rejected rather than ignored, so a typo does not silently
produce a different agent. Command-line flags override spec keys.

### Checking the result

`agent new` validates what it generates before it writes anything. To re-check
later — after you have edited the harness by hand, for example — load it with
the same loader dispatch uses:

```bash
fullsend lock lint-docs --offline
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
fullsend agent list
```

```
NAME       SOURCE
lint-docs  harness/lint-docs.yaml
```

Per-agent overrides compose on top of the generated harness:

```bash
fullsend agent set lint-docs --model sonnet
```

```
  ✓ Set agent "lint-docs": runtime="" model="sonnet" effort="" (empty = inherit)
```

To see what would be generated without writing anything, use `--dry-run`. It
prints the file list and every rendered body:

```bash
fullsend agent new report --dry-run
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
fullsend agent remove link-check
```

```
  ✓ Removed agent "link-check"
```

`agent remove` unregisters the agent; the generated files stay on disk for you
to delete or keep.

### Running it

For the full walkthrough — testing locally with `fullsend run`, dry-running
without a model, and firing the agent in CI — see
[Bring Your Own Agent](../guides/user/bring-your-own-agent.md), starting at
[Quick start](../guides/user/bring-your-own-agent.md#quick-start). Runtime
and model resolve independently (flag, then config, then default); see
[Selecting a runtime and model](../runtimes.md#selecting-a-runtime-and-model).

### Troubleshooting

| Error | Cause | Fix |
|-------|-------|-----|
| `unknown role "scribe"` followed by the role table | The role is not one the hosted mint serves | Use one of the five listed; for a custom role see [Custom Agent Identity](../guides/user/custom-agent-identity.md) |
| `agent name "..." contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)` | The name would not be safe to interpolate into a shell script | Rename. Nothing is written when this fires |
| `these files already exist:` followed by a list | An agent of that name was already generated | Pick another name, or pass `--force`. `--force` never overwrites `policies/` |
| `agent "..." already exists in config` | The name is registered in `config.yaml` | `fullsend agent remove <name>` first. `--force` deliberately does not override this |
| `trigger does not compile: ERROR: <input>:1:5: Syntax error: ...` | A `--trigger` expression is not valid CEL, or does not return a boolean | Compare against the `--on` presets above |
| `unknown --on preset "..."` followed by the preset list | `--on` is not one of the four presets | Use a listed preset, or pass raw CEL with `--trigger` |
| `a trigger is required: pass --on with a preset, or --trigger` | `--trigger ""` was passed explicitly | Give a real trigger. A trigger-less agent is silently never dispatched |
| `no .fullsend directory in the current directory; run from the repository root or pass --fullsend-dir <path>` | `--fullsend-dir` was omitted and the current directory has no `.fullsend` | Run from the repository root or pass `--fullsend-dir`. If the repo has no `.fullsend` yet, scaffold it first |
| `fullsend dir ... does not exist; run ` + "`fullsend github setup`" + ` first` | `--fullsend-dir` points at nothing | Scaffold the repo first |
| `runtime codex takes OpenAI model ids only, and ...: use --model openai/gpt-5.6-luna ...` | `--runtime codex`, or a repo whose `config.yaml` sets `runtime: codex`, with no `--model` or with a model that is not an OpenAI id, such as `opus` | Use `--model openai/<id>` on the same command. Nothing is written when this fires |

These are generation-time errors — `agent new` refuses before writing
anything. For errors from `fullsend run` or in CI (missing credentials,
stale provider profiles, schema validation, triggers that never fire), see
[Bring Your Own Agent § Troubleshooting](../guides/user/bring-your-own-agent.md#troubleshooting).

## `agent add`

Register an agent in config by URL or local path. URL sources are automatically pinned to a specific commit SHA and annotated with a `#sha256=...` integrity hash. When a URL references a branch or tag (rather than a commit SHA), the original ref is stored in the config entry's `ref` field so that subsequent `agent update` calls re-resolve against the same branch. The URL prefix is added to `allowed_remote_resources` if not already present.

```bash
fullsend agent add https://github.com/my-org/agents/blob/main/harness/lint.yaml
fullsend agent add harness/custom-review.yaml --name my-review
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the `.fullsend` configuration directory |
| `--name` | derived from filename | Explicit agent name |

GitHub blob URLs are resolved to pinned `raw.githubusercontent.com` URLs. Branch names containing `/` (for example `.../blob/user/feature/harness/lint.yaml`) are resolved by probing successively longer branch candidates. If no candidate matches, the default branch is used. Non-GitHub URLs must already contain a commit SHA in the path. Local paths must be relative, must not contain path traversal (`..`), and the file must exist. If an agent with the same name already exists, the command fails.

## `agent list`

List all agents registered in config, showing each agent's name and source.

```bash
fullsend agent list
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the `.fullsend` configuration directory |

Read-only. Displays a table with `NAME` and `SOURCE` columns. For URL agents, the `#sha256=...` integrity hash suffix is stripped from the displayed source for readability. Disabled agents (`enabled: false`) are included in the listing.

Example output:
```
NAME     SOURCE
triage   https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml
my-lint  harness/my-lint.yaml
```

## `agent update`

Update a URL-based agent, or a local-path agent's `base:` URL, to a new commit SHA and recompute the `#sha256=...` integrity hash. If no SHA is provided, the branch ref stored at adoption time is re-resolved; if no ref was stored, the default branch HEAD is used. `agent add` never stores a ref for local-path sources, so an `agent update` on a local-path agent's `base:` URL without an explicit SHA always resolves the base repo's default branch — pass an explicit SHA if the `base:` URL was originally pinned to a different branch.

```bash
fullsend agent update triage
fullsend agent update triage a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2
fullsend agent update code
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the `.fullsend` configuration directory |

URL agents are re-pinned in `config.yaml`. Local-path agents whose harness YAML has a `base:` URL are re-pinned in that YAML file; `config.yaml` is left unchanged. Local-path agents without a `base:` URL have nothing to pin. Non-GitHub URLs require an explicit SHA argument. The integrity hash is recomputed by fetching the content at the new SHA.

## `agent set`

Sets `runtime`, `model` and/or `effort` for one agent in `.fullsend/config.yaml` (per-repo
configs). A built-in agent (`triage`, `code`, `review`, `fix`, `retro`, `prioritize`) without an
entry gets a name-only entry; a custom agent's settings land on its `source:` entry (or, for an
agent registered in `config.base.yaml`, on a name-only overlay entry that merges onto it). Only the
flags given change; pass an empty value (`--model ""`) to clear a setting. The result is validated
before it is written.

```bash
fullsend agent set code --runtime claude --model sonnet --effort high
fullsend agent set triage --model xai-vertex/xai/grok-4.6
fullsend agent set review --subagent correctness=opus --subagent default=haiku
```

### Flags

| Flag | Description |
|------|-------------|
| `--fullsend-dir` | Path to the `.fullsend` configuration directory (default `.fullsend`) |
| `--runtime` | Agent runtime for this agent (`claude`, `pi` or `codex`) |
| `--model` | Model for this agent — an alias, a model id, or `provider/id` on pi and codex (codex takes OpenAI ids only) |
| `--effort` | Effort level for this agent (`low`, `medium`, `high`, `xhigh`, `max`) |
| `--subagent` | Per-persona model override as `key=value` (repeatable). Key is a persona name or `default`; value is a model reference. Pass an empty value (`--subagent key=`) to clear an inherited entry — that writes `key: ~` in the config, after which the persona resolves the way an unmentioned one does (its frontmatter model, then `subagents.default`). On pi, a value that resolves to `openai/` prints a warning when the agent's local harness declares no `openai` provider; see [pi § Route a persona to OpenAI](../runtimes/pi.md#route-a-persona-to-openai) |

See [Runtimes — per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort) for precedence.
See [pi § Per-persona model configuration](../runtimes/pi.md#per-persona-model-configuration) for
how `subagents` map to persona dispatch.

## `agent remove`

Remove an agent from config. If the removed agent was the last one using a given `allowed_remote_resources` prefix, that prefix is also cleaned up.

```bash
fullsend agent remove triage
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the `.fullsend` configuration directory |

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — building custom agents and configuring existing ones
- [Default, derived, and custom agents](../guides/user/default-vs-custom.md) — terminology and classification
- [Configuring with skills](../guides/user/customizing-with-skills.md) — extending agents with skills
