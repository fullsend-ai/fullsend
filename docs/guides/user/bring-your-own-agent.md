# Bring Your Own Agent

Add a custom agent to fullsend — or change the configuration of an existing one — from harness file to CI.

This guide covers the end-to-end workflow for building, registering, and dispatching custom agents on GitHub. For details on harness YAML structure and layered resolution, see [Configuring agent behavior](customizing-agents.md).

This guide uses the [fullsend-ai/agents](https://github.com/fullsend-ai/agents) triage agent as a running example.

## Overview

Building and deploying a custom agent takes four steps:

1. **Create the harness and agent definition** — write a harness YAML file that defines _how_ the agent runs and a Markdown file that defines _what_ it does. See [Minimum viable agent](#minimum-viable-agent).
2. **Add a CEL trigger** — write a trigger expression so dispatch knows when to launch your agent. See [CEL Triggers Reference](cel-triggers-reference.md).
3. **Test locally** — run your agent with `fullsend run` before deploying to CI. See [Testing locally](#testing-locally).
4. **Register and deploy** — add your agent to `config.yaml` so dispatch discovers it. See [Registering your agent](#registering-your-agent).

To configure an _existing_ agent instead, see [Configuring existing agents](#configuring-existing-agents).

## How agents work

A fullsend agent has two parts:

1. **Harness file** (YAML) — _how_ the agent runs: sandbox image, policy, scripts, skills, credentials, timeouts.
2. **Agent definition** (Markdown) — _what_ the agent does: prompt, tools, model, skills.

Once registered, your agent runs automatically when a matching GitHub event arrives — an issue is opened, a label is applied, a comment is posted, or a PR is submitted. The harness `trigger` field contains a [CEL expression](cel-triggers-reference.md) that fullsend evaluates against incoming events to decide whether your agent should run:

```
GitHub event (issue opened, label added, PR comment, ...)
        │
        ▼
┌── fullsend dispatch ──────────────────┐
│  1. Normalize event → NormalizedEvent │
│  2. Authorize                         │
│  3. Enumerate registered harnesses    │
│  4. Evaluate CEL triggers             │
│  5. Launch matching agents            │
└───────────────────────────────────────┘
        │
        ▼
┌── harness/my-agent.yaml ────────────┐
│  agent: agents/my-agent.md          │  ◄── prompt & tools
│  trigger: "event.entity.kind == …"  │  ◄── when to run
│  policy: policies/base.yaml         │  ◄── sandbox rules
│  skills: [my-skill]                 │  ◄── domain knowledge
│  pre_script: scripts/pre-...        │  ◄── fetch data (before sandbox)
│  post_script: scripts/post-...      │  ◄── act on output (after sandbox)
└─────────────────────────────────────┘
```

You do not need to write a GitHub Actions workflow file for each custom agent. The dispatch workflow that `fullsend github setup` installs handles discovery and routing for all registered agents.

For local development and debugging, you can also run an agent directly with `fullsend run my-agent` — see [Testing locally](#testing-locally).

**Security model:** agents run inside a sandboxed environment. The sandbox policy enforces filesystem access, landlock, and process identity. Network access is typically managed via **provider profiles** (YAML files in a `providers/` directory) referenced by name in the harness `providers:` list — the scaffold's shared `policies/base.yaml` contains no network rules, since built-in agents use providers. Custom agents can also use inline `network_policies` in a per-agent policy file if providers don't cover their needs. Pre-scripts run on the trusted runner _before_ the sandbox starts; post-scripts run _after_ it exits.

## Minimum viable agent

You need a harness, an agent definition, and supporting scaffold files. If your repo was set up with `fullsend github setup`, the `.fullsend/` directory already contains `policies/`, `providers/`, and `profiles/` from the scaffold — you only need to add `harness/my-agent.yaml` and `agents/my-agent.md`. For a standalone agent repo, copy the scaffold files or create the full layout:

```
.fullsend/
├── harness/my-agent.yaml                  # Execution config (you create)
├── agents/my-agent.md                     # Agent prompt (you create)
├── providers/vertex-ai.yaml               # Provider definition (from scaffold)
├── profiles/fullsend-vertex-ai.yaml       # Profile definition (from scaffold)
└── policies/base.yaml                     # Sandbox policy (from scaffold)
```

**`harness/my-agent.yaml`:**
```yaml
agent: agents/my-agent.md
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest  # Pin to a digest before CI use
policy: policies/base.yaml
providers:
  - vertex-ai
role: my-agent
slug: my-org-my-agent               # GitHub App identity; convention: <org>-<role> (see Custom agent identity)
trigger: |
  event.entity.kind == "work_item"
    && event.transition.kind == "label_changed"
    && event.transition.label.name == "ready-for-my-agent"
    && event.transition.label.action == "added"
timeout_minutes: 15
```

**`providers/vertex-ai.yaml`** — provider definition (declares a provider by name and type):
```yaml
name: vertex-ai
type: fullsend-vertex-ai
credentials:
  _NOOP_VERTEX_AI: ""
```

**`profiles/fullsend-vertex-ai.yaml`** — profile definition (tells OpenShell what endpoints the `fullsend-vertex-ai` type grants access to). Copy this from the scaffold or [fullsend-ai/agents](https://github.com/fullsend-ai/agents):
```yaml
id: fullsend-vertex-ai
display_name: Fullsend Vertex AI
description: Anthropic API and Google Cloud APIs for inference
category: inference
endpoints:
  - host: api.anthropic.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
  - host: "*.googleapis.com"
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
binaries:
  - "**/claude"
  - "**/node"
```

> **Prerequisite (CI only):** for agents running in GitHub Actions, your org or repo must be provisioned for GCP Workload Identity Federation — run [`fullsend inference provision`](../../cli/inference.md) first. The provider profile above controls network access only; real credentials are delivered via `host_files` (see [real-world example](#real-world-example-the-triage-agent)).

**`agents/my-agent.md`:**
````markdown
---
name: my-agent
description: One-line description of what this agent does.
tools: Bash(gh,jq)
model: opus
---

You are my-agent. Your job is to [task description].

## Steps
1. Fetch input from environment variables
2. Analyze and process
3. Write JSON result to `$FULLSEND_OUTPUT_DIR/agent-result.json`

Do NOT push code, create issues, or modify anything directly.
Your only output is the JSON result file.
````

Network access (which APIs the agent can reach) is controlled by provider profiles or inline `network_policies`. The six built-in profiles (`vertex-ai`, `github`, `github-ro`, `github-artifacts`, `gitleaks`, `package-registries`) use framework-known `type` values (e.g. `fullsend-vertex-ai`, `fullsend-github`). To define a fully custom provider type, reference a remote provider definition together with a matching `openshell.profiles` entry (see [Remote Providers and Profiles](customizing-agents.md#remote-providers-and-profiles)). For endpoints not covered by providers, inline `network_policies` in the policy YAML also work. Providers are the pattern used by fullsend's built-in agents, but custom agents can use whichever approach fits.

**Next steps:** [Register your agent](#registering-your-agent) so dispatch discovers it, then [write a CEL trigger](cel-triggers-reference.md#writing-cel-triggers) to control when it runs. To iterate on your agent locally before registering, see [Testing locally](#testing-locally).

## Real-world example: the triage agent

The [fullsend-ai/agents](https://github.com/fullsend-ai/agents) triage agent is a full production agent. The harness below is adapted from the current [`harness/triage.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/triage.yaml) (field order adjusted for readability):

```yaml
agent: agents/triage.md
doc: docs/triage.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/triage.yaml

role: triage
slug: fullsend-ai-triage

host_files:
  - src: common/env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /sandbox/workspace/.gcp-oidc-token
    optional: true
  - src: env/triage.env
    dest: /sandbox/workspace/.env.d/triage.env
    expand: true

skills:
  - skills/issue-labels

pre_script: scripts/pre-triage.sh
post_script: scripts/post-triage.sh

validation_loop:
  script: scripts/validate-output-schema.sh
  schema: schemas/triage-result.schema.json
  max_iterations: 2

timeout_minutes: 10

forge:
  github:
    pre_script: scripts/pre-triage.sh
    post_script: scripts/post-triage.sh
    env:
      runner:
        GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
        GH_TOKEN: ${GH_TOKEN}
      sandbox:
        GITHUB_ISSUE_URL: "${GITHUB_ISSUE_URL}"
        GH_TOKEN: "${GH_TOKEN}"
```

Key patterns to note:

- **`policy: policies/triage.yaml`** is a per-agent policy that includes filesystem, landlock, process, and network rules (via inline `network_policies`). This agent predates the provider-based pattern — new agents can use `providers:` instead (see [Minimum viable agent](#minimum-viable-agent)).
- **`host_files`** copy credentials from the trusted runner into the sandbox. `expand: true` resolves `${VAR}` references before copying.
- **`validation_loop.schema`** references the JSON schema file directly — the validation script checks agent output against it.
- **`forge.github`** scopes scripts, skills, providers, openshell, host_files, and env vars to GitHub. When running on GitLab, a `forge.gitlab` block would take effect instead.
- **`common/env/gcp-vertex.env`** is referenced by relative path because both files live in the same repo. If your agent lives in a different repo, reference it by URL (see [Harness Field Reference — Referencing resources](harness-reference.md#referencing-resources-local-vs-remote)) or copy it locally.

For the complete list of harness fields, see the [Harness Field Reference](harness-reference.md).

## Agent definitions

The agent definition is Markdown with YAML frontmatter:

| Field | Purpose |
|-------|---------|
| `name` | Must match the filename (sans `.md`) |
| `description` | One-line summary |
| `tools` | Allowed Bash commands (e.g., `Bash(gh,jq)`) |
| `model` | LLM model |
| `skills` | Skill names to mount |
| `disallowedTools` | Forbidden Bash patterns |

**Design principles:**
- Agent writes a JSON result file; scripts do all mutations.
- Be specific — define scoring dimensions, thresholds, output schemas.
- Include decision points (branch on confidence, clarity scores, etc.).

## Skills

A skill is a directory with a `SKILL.md` file that teaches the agent domain knowledge:

```
skills/issue-labels/
  SKILL.md            # Required: frontmatter + instructions
  scripts/            # Optional: helper scripts
  references/         # Optional: reference data
```

Reference in the agent frontmatter by name (`skills: [issue-labels]`) and in the harness by path (`skills: [skills/issue-labels]`). Skills can also be URLs with integrity hashes.

## Scripts

Pre and post scripts run on the trusted runner outside the sandbox.

- **Pre-scripts** prepare the environment — fetch data, reset state, write files for `host_files` to copy in.
- **Post-scripts** act on agent output — apply labels, post comments, create PRs.

**Security:** treat agent output as untrusted input. Validate JSON structure, validate field values against allowlists, quote all variables, and limit string lengths.

## Harness composition with `base`

Inherit from an existing harness and override only what differs:

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=abc...

model: sonnet
slug: my-org-triage
skills:
  - skills/my-enhancement
timeout_minutes: 15
```

Base chains support up to 5 levels (`MaxBaseDepth` in `internal/harness/compose.go`). Circular references are detected and rejected. Resolution order: base chain → child overrides → forge selection. See the [Harness Field Reference](harness-reference.md#field-merge-rules-for-base-and-forge) for how each field type combines.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

## Configuring existing agents

You don't need to build from scratch to change how a built-in agent behaves. Use `base` to inherit the built-in harness and override just the fields you want — then register your configured version so it takes precedence.

### Example: add a skill to the code agent

Create a thin harness that inherits from the upstream code agent and adds your skill:

**`harness/code.yaml`:**
```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

skills:
  - skills/my-custom-linting        # Merged with base skills (child overrides by basename)

timeout_minutes: 45                 # Override timeout (scalar → child wins)
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

Test it locally first (see [Testing locally](#testing-locally) for all flags):
```bash
fullsend run code --fullsend-dir .fullsend --target-repo ./my-repo --env-file .env.local
```

Then register it:
```bash
fullsend agent add harness/code.yaml --name code --fullsend-dir .fullsend
```

Because config-registered agents take precedence over built-in agents on name collision, your `code` agent replaces the default — with all of the base agent's scripts, policies, host_files, and plugins still inherited.

### Example: swap the model for review

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/review.yaml#sha256=abc...

model: sonnet
```

### Example: add org-specific environment variables

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

env:
  runner:
    JIRA_TOKEN: "${JIRA_TOKEN}"     # Merged with base env; child keys win
  sandbox:
    JIRA_PROJECT: "MYPROJ"
```

### What you can configure

Any harness field can be overridden. The [Harness Field Reference](harness-reference.md#field-merge-rules-for-base-and-forge) describes how your overrides combine with the base:

- **Change model, timeout, image, scripts** — scalars replace the base value.
- **Add skills** — your entries are merged with the base's by basename; same-named skills override the base entry. **Add plugins or host_files** — your entries are concatenated with the base's.
- **Add or override env vars** — maps are merged; your keys win on collision.
- **Replace validation or security config** — child replaces the entire block.

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
4. **Sub-agents ≠ wrapper skills** — if you need a new review dimension,
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

## Testing locally

Before registering, verify your agent works locally. Use `fullsend run` as a development and debugging tool — it runs your agent directly without going through dispatch:

```bash
fullsend run my-agent \
  --fullsend-dir .fullsend \
  --target-repo ./my-repo \
  --env-file .env.local
```

The `--env-file` supplies variables your harness references (e.g. `GH_TOKEN`, `ANTHROPIC_VERTEX_PROJECT_ID`). See [Running agents locally](running-agents-locally.md) for prerequisites (GCP credentials, sandbox image) and troubleshooting.

Most agents need additional flags for credentials and target repo — see [Running agents locally](running-agents-locally.md) for the full list.

## Registering your agent

Register agents in `config.yaml` so fullsend discovers them. Both per-repo (`.fullsend/config.yaml`) and per-org configs support the `agents:` list. Registration is what makes your agent visible to dispatch — without it, the agent can only be invoked via `fullsend run`.

Authentication for CLI commands uses the `gh` CLI or `GH_TOKEN` environment variable. For URL agents, the CLI resolves GitHub blob URLs to `raw.githubusercontent.com` URLs automatically.

The examples above show customizing built-in agents via `base`. If you've built an entirely new agent from scratch, register it the same way — just point to a local harness instead of a URL.

> **Routing label convention:** Per-repo installs have no prefix constraint; harness agents route via CEL triggers on arbitrary labels. Per-org installs use a managed `dispatch.yml` that routes only through a fixed stage table — custom harness agents are not routed by per-org dispatch regardless of trigger type. If your agent needs custom routing, use a per-repo install. On per-org installs, the workflow-call shim `if:` guard admits every `ready-`-prefixed label, of which only `ready-for-triage`, `ready-to-code`, and `ready-for-review` route to a stage — others such as `ready-for-merge` still reach `dispatch.yml` and exit early.

### CLI

```bash
# Add (auto-pins URL with SHA256):
fullsend agent add \
  https://github.com/fullsend-ai/agents/blob/main/harness/triage.yaml \
  --fullsend-dir .fullsend

# Add local:
fullsend agent add harness/my-agent.yaml --name my-agent --fullsend-dir .fullsend

# List / update / remove:
fullsend agent list --fullsend-dir .fullsend
fullsend agent update triage <sha> --fullsend-dir .fullsend
fullsend agent remove triage --fullsend-dir .fullsend
```

### Per-repo config (`.fullsend/config.yaml`)

```yaml
version: "1"
roles: [triage, coder, review]
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=abc...
  - name: my-cool-agent
    source: harness/my-cool-agent.yaml
allowed_remote_resources:
  - https://raw.githubusercontent.com/fullsend-ai/fullsend/
  - https://raw.githubusercontent.com/fullsend-ai/agents/
```

### Per-org config

```yaml
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage, coder, review]
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=abc...
  - name: my-cool-agent
    source: harness/my-cool-agent.yaml
allowed_remote_resources:
  - https://raw.githubusercontent.com/fullsend-ai/fullsend/
  - https://raw.githubusercontent.com/fullsend-ai/agents/
repos:
  my-repo:
    enabled: true
```

**Notes:**
- `roles` controls which built-in agent roles are enabled. Valid values: `fullsend`, `triage`, `coder`, `review`, `fix`, `retro`, `prioritize`, `e2e`. Custom agents registered via `agents:` do not need to appear in this list.
- URL entries are automatically pinned with `#sha256=...` by `fullsend agent add`.
- URLs must be covered by `allowed_remote_resources` in the same config.
- On name collision, config-registered agents take precedence over built-in agents.
- Individual agents can be disabled with `enabled: false` — see [Disabling Agents](customizing-agents.md#disabling-agents).
- Per-repo config is read from the **base branch**, not from PR branches.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| Agent crashes at 0s | Sandbox can't reach Vertex AI — verify that `providers/vertex-ai.yaml` is listed in your harness `providers:` and that `ANTHROPIC_VERTEX_PROJECT_ID`/`CLOUD_ML_REGION` are set (in your `--env-file` for local runs, or in the workflow `env` block for CI) |
| "role field is required" | Add `role:` to harness |
| Agent can't find input files | Pre-script output paths must match `host_files` entries |
| Provider blocks requests | Check that the required provider profile is listed in `providers:` and exists in the `providers/` directory |
| Schema validation fails | Compare the sandbox output (`$FULLSEND_OUTPUT_DIR/<result>.json`) against the schema referenced in `validation_loop` / `FULLSEND_OUTPUT_SCHEMA`; re-run with `--keep-sandbox` to inspect |
| Agent not found | Verify registration: `fullsend agent list` |
| Agent not triggered by events | Verify your `trigger` expression — see [Verifying your trigger](cel-triggers-reference.md#verifying-your-trigger) |
| `allowed_remote_resources` error | URL agents require a matching prefix in `allowed_remote_resources` — `fullsend agent add` sets this automatically |
| `fullsend run` fails locally | Missing GCP credentials or sandbox image — see [Running agents locally](running-agents-locally.md) |
| Integrity hash mismatch | Remote content changed — run `fullsend agent update <name>` to re-pin |

## See also

- [fullsend-ai/agents](https://github.com/fullsend-ai/agents) — reference implementation used throughout this guide
- [Harness Field Reference](harness-reference.md) — complete harness YAML field reference, merge rules, and resource referencing
- [Custom Agent Identity](custom-agent-identity.md) — using a standalone mint for custom GitHub App identity
- [CEL Triggers Reference](cel-triggers-reference.md) — dispatch flow, NormalizedEvent fields, transition kinds, and trigger patterns
- [Configuring with Skills](customizing-with-skills.md) — creating and managing skills; [authoring augmentations](customizing-with-skills.md#authoring-skills-that-augment-defaults)
- [`author-fullsend-augmentations` skill](../../../skills/author-fullsend-augmentations/SKILL.md) — discovery-driven guide for writing skills and sub-agents that complement shipped defaults
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring agent behavior](customizing-agents.md) — harness configurations and `base:` composition
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when configuration crosses into custom agent territory
- [Escalation ladder](../../agents/topics/escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Standalone mint](../infrastructure/standalone-mint.md) — custom agent roles and identity
