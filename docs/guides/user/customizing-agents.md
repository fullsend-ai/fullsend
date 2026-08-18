# Configuring Agent Behavior

This guide explains how to configure existing fullsend agents for your
organization and repositories. It covers harness-based configuration with
`base:` composition, environment variables, status notifications, and
disabling agents.

For a quick overview of all customization options, see
[Customizing Agents](customizing-overview.md). For the complete harness field
reference, see [Harness Reference](harness-reference.md).

## What you can configure

You can customize any existing agent without building one from scratch.
Common configuration goals:

| Goal | How |
|------|-----|
| Change model, timeout, or image | Override scalar fields via `base:` composition |
| Add org-specific skills | Add entries to the `skills:` list |
| Add environment variables | Add entries under `env.runner` or `env.sandbox` |
| Extend the sandbox with packages | Build a custom image and set `image:` |
| Add executables to the sandbox | Use `host_files` to copy scripts to `/sandbox/workspace/bin` |
| Disable a built-in agent | Set `enabled: false` in `config.yaml` |
| Control status comments | Configure `status_notifications` in `config.yaml` |
| Teach agents your conventions | Use [AGENTS.md](customizing-with-agents-md.md) (no harness change needed) |
| Give an agent domain knowledge | Use [skills](customizing-with-skills.md) (no harness change needed) |

## Configuration with `base:` composition

The primary mechanism for customizing agents. Register agents in
`config.yaml` with a `base:` URL pointing to the upstream harness, and
override only the fields that differ.

### Example: add a skill to the code agent

Create a thin harness that inherits from the upstream code agent and adds your skill:

**`harness/code.yaml`:**
```yaml
base: "https://github.com/fullsend-ai/agents/tree/main/harness/code.yaml#sha256=..."

skills:
  - skills/my-custom-linting        # Merged with base skills (child overrides by basename)

timeout_minutes: 45                 # Override timeout (scalar: child wins)
```

**`skills/my-custom-linting/SKILL.md`:**
```markdown
---
name: my-custom-linting
description: Org-specific linting rules and conventions.
---

# My Custom Linting

[Your skill content...]
```

Register the agent in `config.yaml`:

```yaml
agents:
  - name: code
    source: harness/code.yaml
```

Because config-registered agents take precedence over built-in agents on name collision, your `code` agent replaces the default — with all of the base agent's scripts, policies, host_files, and plugins still inherited.

Test it locally first:
```bash
fullsend run code --fullsend-dir .fullsend --target-repo ./my-repo --env-file .env.local
```

See [Running agents locally](running-agents-locally.md) for prerequisites and troubleshooting.

### Example: swap the model for review

```yaml
base: "https://github.com/fullsend-ai/agents/tree/main/harness/review.yaml#sha256=..."

model: sonnet
```

### Example: add org-specific environment variables

```yaml
base: "https://github.com/fullsend-ai/agents/tree/main/harness/code.yaml#sha256=..."

env:
  runner:
    JIRA_TOKEN: "${JIRA_TOKEN}"     # Merged with base env; child keys win
  sandbox:
    JIRA_PROJECT: "MYPROJ"
```

### What you can override

Any harness field can be overridden. See the [field merge rules](harness-reference.md#field-merge-rules) for how each field type combines with the base:

- **Change model, timeout, image, scripts** — scalars replace the base value.
- **Add skills** — your entries are merged with the base's by basename; same-named skills override the base entry. **Add plugins or host_files** — your entries are concatenated with the base's.
- **Add or override env vars** — maps are merged; your keys win on collision.
- **Replace validation or security config** — child replaces the entire block.

Base chains support up to 5 levels. Circular references are detected and rejected. Resolution order: base chain, child overrides, forge selection.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own.

### Tuning agents with augmentation skills

Before you fork a whole agent or replace a built-in skill, decide what you
are actually changing:

| Goal | Prefer |
|------|--------|
| Domain rules, linting, or constraints that sit *alongside* defaults | A **unique-named** augmentation skill (append via harness `skills:`) |
| Shorter or reformatted human-facing output (comments, summaries) | Augmentation skill with **field ownership** and hard limits — not soft "be concise" |
| New review dimension under an orchestrator (for example `pr-review`) | A **sub-agent** file under that skill's `sub-agents/`, plus whatever registration the current platform requires |
| Replace most of a skill's procedure | Whole-skill override / derived harness — heavier; you stop inheriting upstream edits |

**What to think about when authoring:**

1. **Discover first** — read the target agent's harness, agent definition,
   schema, post-script, and any shipped skills under
   [fullsend-ai/agents](https://github.com/fullsend-ai/agents). Do not guess
   field names or roster lists from memory.
2. **Unique skill names** — a repo skill with the same directory name as a
   built-in is ignored (see [skill precedence](customizing-with-skills.md#skill-precedence)).
3. **Specificity wins** — vague augmentations lose to hard default
   instructions. Own exact fields; use word limits and templates.
4. **Sub-agents are not wrapper skills** — if you need a new review dimension,
   ship `sub-agents/<name>.md` (and parent dispatch updates when the
   orchestrator uses a fixed roster). Do not invent a parallel
   `*/SKILL.md` that embeds the same content.
5. **Prefer the lightest shipping path** the current docs support —
   upstream contribution, file-level skill override when available, or
   whole-skill fork only when that is still required.

> **Planned:** File-level overrides inside a pinned skill directory (add or
> replace a single `sub-agents/<name>.md` without vendoring the whole tree)
> are tracked in [#6158](https://github.com/fullsend-ai/fullsend/issues/6158)
> / [#6157](https://github.com/fullsend-ai/fullsend/issues/6157). Until that
> lands, fixed-roster sub-agent changes usually need an upstream PR or a
> whole-skill pin.

**Authoring help:** the contributor skill
[`author-fullsend-augmentations`](../../../skills/author-fullsend-augmentations/SKILL.md)
walks this discovery and conflict analysis. Use it when writing or reviewing
augmentation skills and sub-agents. Details also live in
[Configuring with skills](customizing-with-skills.md#authoring-skills-that-augment-defaults).

## Configuration examples

### Extending the sandbox image

When `host_files` injection is not enough and you need additional packages or
toolchains in the sandbox, build an image that extends the published base and
point your harness `image:` field at it:

```dockerfile
FROM ghcr.io/fullsend-ai/fullsend-sandbox:latest
RUN apt-get update && apt-get install -y --no-install-recommends rustc \
  && rm -rf /var/lib/apt/lists/*
```

Use `ghcr.io/fullsend-ai/fullsend-code:latest` as the parent instead when you
also need the Go toolchain. Pin the parent tag to a digest before CI use.

### Adding executables

The sandbox already has `/sandbox/workspace/bin` on its `PATH`. To make a
script available as a command, drop it there:

1. Create your script (e.g. `scripts/my-tool.sh`):

   ```bash
   #!/bin/bash
   echo "Hello from $0"
   ```

2. Make it executable with `chmod +x scripts/my-tool.sh`.
3. Map it into the sandbox via `host_files`:

   ```yaml
   host_files:
     - src: scripts/my-tool.sh
       dest: /sandbox/workspace/bin/my-tool.sh
   ```

4. The agent should be able to run `my-tool.sh` directly.

#### Modifying the PATH for external toolchains

When you need a directory outside `/sandbox/workspace/bin` on the `PATH`
(e.g. an external toolchain), use a `.env.d` file:

1. Create an env file (e.g. `env/extra-path.env`):

   ```bash
   PATH=/opt/my-toolchain/bin:$PATH
   ```

2. Map it into the sandbox:

   ```yaml
   host_files:
     - src: env/extra-path.env
       dest: /sandbox/workspace/.env.d/extra-path.env
   ```

3. At startup the sandbox sources every `*.env` file under
   `/sandbox/workspace/.env.d/`, picking up your PATH addition.

**Note**: `env.sandbox` cannot modify `PATH`, the harness ignores special
variables to protect sandbox operation.

### Adding a skill

Create `skills/my-skill/SKILL.md` in your `.fullsend` config repo or agents repo:

```markdown
# My Custom Skill

Custom domain knowledge for this organization.

## Examples

...
```

Reference the skill in your harness's `skills:` list. The skill is available to all agents that include it in their harness configuration. See [Configuring with Skills](customizing-with-skills.md) for details on skill authoring and precedence.

## Agent roles

Each agent role has its own identity, permissions, and purpose:

| Role | GitHub App | Purpose |
|------|------------|---------|
| `fullsend` | `{org}-fullsend[bot]` | Dispatch/control |
| `triage` | `{org}-triage[bot]` | Issue triage |
| `coder` | `{org}-coder[bot]` | Code generation |
| `review` | `{org}-review[bot]` | PR review |
| `fix` | (reuses coder app) | Fix failures |
| `retro` | `{org}-retro[bot]` | Retrospectives |
| `prioritize` | `{org}-prioritize[bot]` | Backlog priority |

**Naming conventions:**
- App naming: `{org}-{role}`
- Bot naming: `{org}-{role}[bot]`
- PEM storage: GCP Secret Manager or filesystem (standalone)
- Secret name: `fullsend-{role}-app-pem`

> **Note:** The "fix" role reuses the "coder" app and PEM — no separate GitHub App or secret is created for it.

> **Note:** Mint-only dogfood roles such as `scribe` can be registered with
> `fullsend mint add-role` (and used via remote harness registration) but are
> **not** valid values for the `.fullsend` `roles:` field until scaffold /
> workflow wiring lands. Adding them under `roles:` fails config validation
> rather than silently no-oping.

## Status notifications

Agent workflows post status comments on issues and PRs when they start and complete. Control this with `status_notifications` in `config.yaml`.

For per-org installs, nest it under `defaults`:

```yaml
defaults:
  status_notifications:
    comment:
      start: enabled      # "enabled" (default) | "disabled"
      completion: enabled  # "enabled" (default) | "on_failure" | "disabled"
```

For per-repo installs, set it at the top level of `.fullsend/config.yaml`:

```yaml
status_notifications:
  comment:
    start: enabled
    completion: enabled
```

When `status_notifications` is omitted entirely, both start and completion comments default to enabled.

### Completion modes

| Value | Behavior |
|-------|----------|
| `enabled` | Always post a completion comment (default) |
| `on_failure` | Post when the agent fails, is cancelled, or is skipped by a pre-script; the start comment is automatically suppressed to avoid notification noise |
| `disabled` | Never post a completion comment; silently remove the start comment |

`on_failure` is useful when you want to reduce notification noise — successful runs leave no trace, but failures still surface. When `completion` is set to `on_failure`, the start comment is automatically suppressed regardless of the `start` setting, because posting and then deleting a start comment would still trigger a GitHub notification pointing to a deleted comment.

In `enabled` mode (the default), a hard crash or cancellation that happens before the agent could post anything at all is also surfaced after the fact: a post-job cleanup step synthesizes an "Interrupted" comment so the run doesn't silently vanish.

## Disabling agents

To disable an agent (including built-in scaffold agents) without removing
its role, add an entry with `enabled: false` in your config:

```yaml
agents:
  - name: retro
    enabled: false
```

This prevents the agent from dispatching and from resolving via
`fullsend run`. The role can stay in `defaults.roles` — only the agent
is suppressed. Omitting `enabled` (or setting it to `true`) keeps the
agent active (backward compatible).

When multiple entries share a name, the last writer wins. This allows a
disable-then-enable pattern to replace a default agent with a custom one:

```yaml
agents:
  - name: retro
    enabled: false
  - name: retro
    source: harness/custom-retro.yaml
    enabled: true
```

**Important:** The `name` must match the **agent/harness name**, not the
role name. The built-in agent names are: `code`, `triage`, `review`,
`fix`, `retro`, `prioritize`. Note that the role `coder` maps to the
agent named `code` — writing `name: coder` passes validation but
disables nothing because no agent has that harness name.

## See also

- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [Harness Reference](harness-reference.md) — complete harness field reference, merge rules, and advanced configuration
- [Bring Your Own Agent](bring-your-own-agent.md) — building and registering custom agents from scratch
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring with Skills](customizing-with-skills.md) — extending agents with skills
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when does configuration cross into derived or custom agent territory?
- [Escalation ladder](../../agents/topics/escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Getting Started](../getting-started/) — initial setup
- [Bugfix Workflow](bugfix-workflow.md) — how agents work together
- [Standalone Mint](../infrastructure/standalone-mint.md) — running your own mint with custom agent roles
