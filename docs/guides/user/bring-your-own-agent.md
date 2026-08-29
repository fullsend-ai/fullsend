# Bring Your Own Agent

Add a custom agent to fullsend — from harness file to CI. This guide covers
the end-to-end workflow for building, registering, and dispatching custom
agents on GitHub.

To configure an existing agent (model, timeout, skills, env vars) without
building from scratch, see
[Configuring Agent Behavior](customizing-agents.md). For a quick overview of
all customization options, see
[Customizing Agents](customizing-overview.md).

This guide uses the [fullsend-ai/agents](https://github.com/fullsend-ai/agents) triage agent as a running example.

## Overview

Building and deploying a custom agent takes four steps:

1. **Create the harness and agent definition** — write a harness YAML file that defines _how_ the agent runs and a Markdown file that defines _what_ it does. See [Minimum viable agent](#minimum-viable-agent).
2. **Add a CEL trigger** — write a trigger expression so dispatch knows when to launch your agent. See [CEL Triggers Reference](cel-triggers-reference.md).
3. **Test locally** — run your agent with `fullsend run` before deploying to CI. See [Testing locally](#testing-locally).
4. **Register and deploy** — add your agent to `config.yaml` so dispatch discovers it. See [Registering your agent](#registering-your-agent).

## Before you begin

- **fullsend CLI** installed and available on your PATH.
- **Repository scaffolded.** Run [`fullsend github setup`](../getting-started/configuring-github.md) first — it creates the `.fullsend/` directory with `policies/`, `providers/`, and `profiles/` from the scaffold. For a standalone agent repo, you can create these files manually (see [Minimum viable agent](#minimum-viable-agent)).
- **GCP inference provisioned (CI only).** For agents running in GitHub Actions, run [`fullsend inference provision`](../../cli/inference.md) to set up Workload Identity Federation.
- **GitHub Apps installed (CI only).** Your org needs the fullsend GitHub Apps — see [Configuring GitHub](../getting-started/configuring-github.md).

## How agents work

A fullsend agent has two parts:

1. **Harness file** (YAML) — _how_ the agent runs: sandbox image, policy, scripts, skills, credentials, timeouts.
2. **Agent definition** (Markdown) — _what_ the agent does: prompt, tools, model, skills.

Once registered, your agent runs automatically when a matching GitHub event arrives — an issue is opened, a label is applied, a comment is posted, or a PR is submitted. The harness `trigger` field contains a [CEL expression](cel-triggers-reference.md) that fullsend evaluates against incoming events to decide whether your agent should run:

```
GitHub event (issue opened, label added, PR comment, ...)
        |
        v
+-- fullsend dispatch ----------------------+
|  1. Normalize event -> NormalizedEvent    |
|  2. Authorize                            |
|  3. Enumerate registered harnesses       |
|  4. Evaluate CEL triggers                |
|  5. Launch matching agents               |
+-------------------------------------------+
        |
        v
+-- harness/my-agent.yaml ------------------+
|  agent: agents/my-agent.md               |  <-- prompt & tools
|  trigger: "event.entity.kind == ..."     |  <-- when to run
|  policy: policies/base.yaml             |  <-- sandbox rules
|  skills: [my-skill]                      |  <-- domain knowledge
|  pre_script: scripts/pre-...            |  <-- fetch data (before sandbox)
|  post_script: scripts/post-...          |  <-- act on output (after sandbox)
+-------------------------------------------+
```

You do not need to write a GitHub Actions workflow file for each custom agent. The dispatch workflow that `fullsend github setup` installs handles discovery and routing for all registered agents.

For local development and debugging, you can also run an agent directly with `fullsend run my-agent` — see [Testing locally](#testing-locally).

**Security model:** agents run inside a sandboxed environment. The sandbox policy enforces filesystem access, landlock, and process identity. Network access is typically managed via **provider profiles** (YAML files in a `providers/` directory) referenced by name in the harness `providers:` list — the scaffold's shared `policies/base.yaml` contains no network rules, since built-in agents use providers. Custom agents can also use inline `network_policies` in a per-agent policy file if providers don't cover their needs. Pre-scripts run on the trusted runner _before_ the sandbox starts; post-scripts run _after_ it exits.

## Minimum viable agent

You need a harness, an agent definition, and supporting scaffold files. If your repo was set up with `fullsend github setup`, the `.fullsend/` directory already contains `policies/`, `providers/`, and `profiles/` from the scaffold — you only need to add `harness/my-agent.yaml` and `agents/my-agent.md`. For a standalone agent repo, copy the scaffold files or create the full layout:

```
.fullsend/
+-- harness/my-agent.yaml                  # Execution config (you create)
+-- agents/my-agent.md                     # Agent prompt (you create)
+-- providers/vertex-ai.yaml               # Provider definition (from scaffold)
+-- profiles/fullsend-vertex-ai.yaml       # Profile definition (from scaffold)
+-- policies/base.yaml                     # Sandbox policy (from scaffold)
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

> **Note (CI only):** the provider profile above controls network access only; real credentials are delivered via `host_files` (see [real-world example](#real-world-example-the-triage-agent)). Make sure you've completed the GCP prerequisites in [Before you begin](#before-you-begin).

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

Network access (which APIs the agent can reach) is controlled by provider profiles or inline `network_policies`. The six built-in profiles (`vertex-ai`, `github`, `github-ro`, `github-artifacts`, `gitleaks`, `package-registries`) use framework-known `type` values (e.g. `fullsend-vertex-ai`, `fullsend-github`). To define a fully custom provider type, reference a remote provider definition together with a matching `openshell.profiles` entry (see [Remote providers and profiles](customizing-agents.md#remote-providers-and-profiles)). For endpoints not covered by providers, inline `network_policies` in the policy YAML also work. Providers are the pattern used by fullsend's built-in agents, but custom agents can use whichever approach fits.

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

overlays:
- when: 'runtime.forge == "github"'
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
- **`overlays`** uses CEL `when` expressions to conditionally apply scripts, skills, providers, openshell, host_files, and env vars. Resolution merges all matching entries in order: every entry whose `when` evaluates to true is applied, with later matches taking precedence over earlier ones. The CEL environment exposes `event` (the triggering event), `runtime.forge` (the effective forge platform), and `config` (per-repo config from config.yaml). When running without an event context (e.g., `fullsend run` or `fullsend lock`), `event` is an empty map — use `has(event.source)` to guard event field access: `has(event.source) && event.source.system == "jira"` instead of just `event.source.system == "jira"` to avoid "no such key" errors.
- **`common/env/gcp-vertex.env`** is referenced by relative path because both files live in the same repo. If your agent lives in a different repo, reference it by URL (see [Harness Field Reference — Referencing resources](../../reference/harness-reference.md#referencing-resources-local-vs-remote)) or copy it locally.

For the complete list of harness fields, see the [Harness Field Reference](../../reference/harness-reference.md).

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

When writing the agent body:
- The agent writes a JSON result file; scripts handle all mutations.
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

Reference in the agent frontmatter by name (`skills: [issue-labels]`) and in the harness by path (`skills: [skills/issue-labels]`). Skills can also be URLs with integrity hashes. See [Configuring with Skills](customizing-with-skills.md) for details on creating and managing skills.

For details on skill authoring, precedence, and extension points, see
[Configuring with Skills](customizing-with-skills.md).

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

Base chains support up to 5 levels (`MaxBaseDepth` in `internal/harness/compose.go`). Circular references are detected and rejected. Resolution order: base chain, child overrides, overlay resolution. See the [Harness Field Reference](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays) for how each field type combines.

> **Overlay precedence with `base:`:** Overlays are concatenated base-first, child-appended — the same ordering as `plugins`, `providers`, and `api_servers`. Because `ResolveOverlays` merges all matching entries in order (later matches take precedence), child overlay entries override base overlay entries with the same condition. This follows the child-overrides-base convention used by scalar and map merges.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

To configure an existing agent without building from scratch, see [Configuring Agent Behavior](customizing-agents.md#configuration-with-base-composition).

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

Register agents in `.fullsend/config.yaml` so fullsend discovers them. Registration is what makes your agent visible to dispatch — without it, the agent can only be invoked via `fullsend run`.

Authentication for CLI commands uses the `gh` CLI or `GH_TOKEN` environment variable. For URL agents, the CLI resolves GitHub blob URLs to `raw.githubusercontent.com` URLs automatically.

Harness agents route via CEL triggers on arbitrary labels — there is no prefix constraint.

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

### Config file (`.fullsend/config.yaml`)

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

- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [fullsend-ai/agents](https://github.com/fullsend-ai/agents) — reference implementation used throughout this guide
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML field reference, merge rules, and resource referencing
- [Custom Agent Identity](custom-agent-identity.md) — using a standalone mint for custom GitHub App identity
- [CEL Triggers Reference](cel-triggers-reference.md) — dispatch flow, NormalizedEvent fields, transition kinds, and trigger patterns
- [Configuring with Skills](customizing-with-skills.md) — creating and managing skills; [authoring augmentations](customizing-with-skills.md#authoring-skills-that-augment-defaults)
- [`author-fullsend-augmentations` skill](../../../skills/author-fullsend-augmentations/SKILL.md) — discovery-driven guide for writing skills and sub-agents that complement shipped defaults
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring Agent Behavior](customizing-agents.md) — harness configuration and `base:` composition
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when configuration crosses into custom agent territory
- [Escalation ladder](../../agents/topics/escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Standalone mint](../infrastructure/standalone-mint.md) — custom agent roles and identity
