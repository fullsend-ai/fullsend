# Bring Your Own Agent

Add a custom agent to fullsend — or change the configuration of an existing one — from harness file to CI.

This guide covers the end-to-end workflow for building, registering, and dispatching custom agents on GitHub. For details on harness YAML structure and layered resolution, see [Configuring agent behavior](customizing-agents.md).

This guide uses the [fullsend-ai/agents](https://github.com/fullsend-ai/agents) triage agent as a running example.

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
slug: my-org-my-agent               # GitHub App identity; convention: <org>-<role> (see Advanced: custom identity)
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
- **`overlays`** uses CEL `when` expressions to conditionally apply scripts, skills, providers, openshell, host_files, and env vars. Resolution is first-match-wins: the first entry whose `when` evaluates to true is merged; remaining entries are skipped. The CEL environment exposes `event` (the triggering event), `runtime.forge` (the effective forge platform), and `config` (per-repo config from config.yaml).
- **`common/env/gcp-vertex.env`** is referenced by relative path because both files live in the same repo. If your agent lives in a different repo, reference it by URL (see [Remote references](#referencing-resources-local-vs-remote)) or copy it locally.

## Harness field reference

```yaml
# ── Required ──────────────────────────────────────────────────
agent: agents/my-agent.md           # Path to agent definition
role: my-agent                      # Role name (lowercase letter first, then a-z, 0-9, _, -; no double hyphens)

# ── Identity & metadata ──────────────────────────────────────
slug: my-org-my-role                # GitHub App identity (convention: <org>-<role>)
description: One-line summary       # Human-readable description
doc: docs/agents/my-agent.md        # Source-repo-only; not resolved at runtime
trigger: "event.entity.kind == 'work_item'"  # Optional CEL expression over NormalizedEvent (see CEL Triggers Reference)

# ── Composition ───────────────────────────────────────────────
base: harness/common-base.yaml      # Inherit from another harness (local or URL)

# ── Sandbox ───────────────────────────────────────────────────
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/base.yaml          # Sandbox policy (filesystem, landlock, process)
model: opus                         # LLM model override
effort: high                        # Reasoning effort (low, medium, high, xhigh, max); claude runtime only
readonly_repo: false                # Mount repo as read-only in sandbox
providers:                           # Network access via provider profiles
  - vertex-ai                       # References providers/vertex-ai.yaml
  - github                          # References providers/github.yaml

# ── Skills & plugins ──────────────────────────────────────────
skills:
  - skills/my-skill                  # Local path or URL with #sha256=...
plugins:
  - plugins/gopls-lsp                # Local path or URL with #sha256=...
openshell:                           # OpenShell sandbox profiles
  profiles:
    - https://example.com/profile.yaml#sha256=abc...

# ── Scripts (local paths only) ────────────────────────────────
pre_script: scripts/pre-my-agent.sh
post_script: scripts/post-my-agent.sh
agent_input: inputs/my-input.md     # File passed as initial input to the agent

# ── Validation ────────────────────────────────────────────────
validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2
  feedback_mode: stderr              # How validation feedback reaches the agent

# ── Host files ────────────────────────────────────────────────
host_files:
  - src: env/my-agent.env            # Runner path (supports ${VAR})
    dest: /sandbox/workspace/.env.d/my-agent.env
    expand: true                     # Resolve ${VAR} in contents
  - src: ${SOME_CREDENTIAL}
    dest: /tmp/.cred.json
    optional: true                   # Skip if missing

# ── Environment ───────────────────────────────────────────────
env:
  runner:                            # Available to pre/post scripts
    MY_VAR: "${MY_VAR}"
  sandbox:                           # Available inside sandbox
    MY_SETTING: "value"
runner_env:                          # ⚠ Deprecated: use env.runner instead
  MY_VAR: "${MY_VAR}"

# ── Timeouts ──────────────────────────────────────────────────
timeout_minutes: 20
sandbox_timeout_seconds: 300         # 30-600

# ── Remote resources ──────────────────────────────────────────
allowed_remote_resources:
  - https://github.com/my-org/agent-library/
allow_runtime_fetch: true
max_runtime_fetches: 10

# ── API servers ───────────────────────────────────────────────
api_servers:                         # Host-side REST proxies exposed to sandbox
  - name: my-api
    script: scripts/api-server.sh    # Local script that runs the server
    port: 8080                       # Port the sandbox connects to
    env:                             # Env vars for the server process
      API_KEY: "${API_KEY}"

# ── Conditional overrides (CEL-guarded, first-match-wins) ────
overlays:
- when: 'event.source.system == "jira" && runtime.forge == "github"'
  pre_script: scripts/pre-jira-on-gh.sh
  skills: [skills/jira-read]          # Merged with top-level
  env:
    runner:
      GH_TOKEN: "${GH_TOKEN}"
      JIRA_TOKEN: "${JIRA_TOKEN}"
- when: 'runtime.forge == "github"'
  pre_script: scripts/pre-gh.sh
  post_script: scripts/post-gh.sh
  skills: [skills/github-specific]    # Merged with top-level
  providers: [providers/github.yaml]  # Concatenated with top-level
  openshell:
    profiles: [profiles/github.yaml]  # Concatenated with top-level
  host_files:                         # Overlay-specific host files
    - src: env/github.env
      dest: /run/secrets/forge.env
  env:
    runner:
      GH_TOKEN: "${GH_TOKEN}"
- when: 'event.source.system == "jira"'
  pre_script: scripts/pre-jira.sh

# ── Security ──────────────────────────────────────────────────
security:
  fail_mode: closed                  # "closed" (default) or "open"
```

> **Naming convention:** Prefix settings that tune one agent's behavior with
> that agent's role in caps, e.g. `REVIEW_SEVERITY_THRESHOLD` — this avoids
> collisions when multiple agents share a sandbox or env file.
>
> A setting meant to apply the same way across every agent (like
> `roles` or `create_issues.allow_targets`) belongs in `config.yaml`
> instead, not as an env var.

### Deprecated fields

> **Deprecated:** `forge` is deprecated. Use `overlays` with CEL `when`
> expressions instead (see [ADR 0088](../../ADRs/0088-cel-guarded-overlays.md)).
> The `forge` field still works but emits a deprecation warning at lint time.
> Migration: each forge key becomes an overlay entry — e.g. `forge: github:`
> becomes `overlays: - when: 'runtime.forge == "github"'`. Note the conditioning
> axis: `runtime.forge` reflects the effective forge platform (from `--forge`
> flag, `config.forge`, or CI env vars), while `event.source.system` identifies
> the event origin. These diverge for cross-system events (e.g. a JIRA issue
> triggering work on GitHub). `forge` and `overlays` cannot coexist in the
> same harness.

> **Deprecated:** `runner_env` is deprecated. Use `env.runner`
> instead. The `runner_env` field still works but emits a deprecation warning
> at runtime. Migration: move `runner_env:` entries under `env: runner:` and
> delete the `runner_env:` block.

### Field merge rules (for `base` and `overlays`)

Overlays use first-match-wins: exactly one overlay (or none) applies to any
given event. When an agent needs config from multiple concerns (e.g.
JIRA-specific scripts *and* GitHub-specific runner env), create a combined
entry. More-specific entries go first; broader fallbacks go last.

| Field type | Behavior |
|-----------|----------|
| Scalars (`model`, `pre_script`, `policy`, `image`, etc.) | Child wins if non-empty |
| `skills` | Merged with deduplication by basename (child overrides base) |
| `providers`, `openshell.profiles` | Concatenated (base + child); also applies per matched overlay |
| `plugins`, `api_servers` | Concatenated (base + child) |
| `host_files` | Concatenated; child overrides by `dest` |
| `env`, `runner_env` (deprecated) | Merged; child keys win |
| `validation_loop`, `security` | Child replaces entirely |
| `allowed_remote_resources`, `allow_runtime_fetch`, `max_runtime_fetches` | NOT inherited (child must declare its own) |

### Referencing resources: local vs. remote

**Local paths** resolve relative to the harness file's base directory:
```yaml
agent: agents/triage.md              # → {base}/agents/triage.md
```

**Remote URLs** require a `#sha256=...` integrity hash:
```yaml
agent: https://raw.githubusercontent.com/org/repo/<sha>/agents/lint.md#sha256=abc...
```

**Scripts are local-only** — `pre_script`, `post_script`, and `validation_loop.script` must be local paths (they run on the trusted runner). Exception: scripts declared in a `base` harness fetched via URL are allowed.

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

Base chains support up to 5 levels (`MaxBaseDepth` in `internal/harness/compose.go`). Circular references are detected and rejected. Resolution order: base chain → child overrides → overlay resolution. See [field merge rules](#field-merge-rules-for-base-and-overlays) for how each field type combines.

> **Overlay precedence with `base:`:** Overlays are concatenated base-first, child-appended — the same ordering as `plugins`, `providers`, and `api_servers`. Because `ResolveOverlays` uses first-match-wins, a base overlay whose `when` matches will take precedence over a child overlay with the same condition. This is consistent with the trusted-base model (base URLs require an org-level allowlist).

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

Any harness field can be overridden. The [field merge rules](#field-merge-rules-for-base-and-overlays) determine how your overrides combine with the base:

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

## Advanced: custom identity

By default, agents authenticate using shared fullsend GitHub Apps via the `slug` field. If you need your own GitHub App — for custom permissions, compliance, or branding — you can run a **standalone mint**. Follow the [Standalone mint guide](../infrastructure/standalone-mint.md) to set one up.

Once your standalone mint is running, configure your agent to use it:

1. **Reference your role in the harness:**
   ```yaml
   role: my-role
   slug: my-org-my-role
   ```

2. **Set `FULLSEND_MINT_URL`** in your repo to point to your standalone mint.

When configured with `FALLBACK_MINT_URL`, the standalone mint serves custom roles locally while proxying unhandled roles to the hosted mint (see [Standalone mint — Fallback proxy behavior](../infrastructure/standalone-mint.md#fallback-proxy-behavior)).

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
- [CEL Triggers Reference](cel-triggers-reference.md) — dispatch flow, NormalizedEvent fields, transition kinds, and trigger patterns
- [Configuring with Skills](customizing-with-skills.md) — creating and managing skills; [authoring augmentations](customizing-with-skills.md#authoring-skills-that-augment-defaults)
- [`author-fullsend-augmentations` skill](../../../skills/author-fullsend-augmentations/SKILL.md) — discovery-driven guide for writing skills and sub-agents that complement shipped defaults
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring agent behavior](customizing-agents.md) — harness configurations and `base:` composition
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when configuration crosses into custom agent territory
- [Escalation ladder](../../agents/topics/escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Standalone mint](../infrastructure/standalone-mint.md) — custom agent roles and identity
