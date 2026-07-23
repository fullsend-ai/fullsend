# Bring Your Own Agent

Add a custom agent to fullsend — or change the configuration of an existing one — from harness file to CI.

This guide covers the end-to-end workflow for building, registering, and dispatching custom agents on GitHub. For details on harness YAML structure and layered resolution, see [Customizing agents](customizing-agents.md).

This guide uses the [fullsend-ai/agents](https://github.com/fullsend-ai/agents) triage agent as a running example.

## How agents work

A fullsend agent has two parts:

1. **Harness file** (YAML) — _how_ the agent runs: sandbox image, policy, scripts, skills, credentials, timeouts.
2. **Agent definition** (Markdown) — _what_ the agent does: prompt, tools, model, skills.

Once registered, your agent runs automatically when a matching GitHub event arrives — an issue is opened, a label is applied, a comment is posted, or a PR is submitted. The harness `trigger` field contains a [CEL expression](#writing-cel-triggers) that fullsend evaluates against incoming events to decide whether your agent should run:

```
GitHub event (issue opened, label added, PR comment, ...)
        │
        ▼
┌── fullsend dispatch ──────────────────┐
│  1. Normalize event → NormalizedEvent │
│  2. Authorize (ADR 0054)              │
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

**Security model:** agents run inside a sandboxed environment. The sandbox policy enforces filesystem access, landlock, and process identity. Network access is typically managed via **provider profiles** (YAML files in a `providers/` directory) referenced by name in the harness `providers:` list — the scaffold's shared `policies/base.yaml` contains no network rules, since built-in agents use providers ([ADR 0065](../../ADRs/0065-provider-backed-policy-composition.md)). Custom agents can also use inline `network_policies` in a per-agent policy file if providers don't cover their needs. Pre-scripts run on the trusted runner _before_ the sandbox starts; post-scripts run _after_ it exits.

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

Network access (which APIs the agent can reach) is controlled by provider profiles or inline `network_policies`. The six built-in profiles (`vertex-ai`, `github`, `github-ro`, `github-artifacts`, `gitleaks`, `package-registries`) use framework-known `type` values (e.g. `fullsend-vertex-ai`, `fullsend-github`). To define a fully custom provider type, reference a remote provider definition together with a matching `openshell.profiles` entry (see [Remote Providers and Profiles](customizing-agents.md#remote-providers-and-profiles) and [ADR 0070](../../ADRs/0070-portable-provider-profile-resolution.md)). For endpoints not covered by providers, inline `network_policies` in the policy YAML also work. Providers are the pattern used by fullsend's built-in agents ([ADR 0065](../../ADRs/0065-provider-backed-policy-composition.md)), but custom agents can use whichever approach fits.

**Next steps:** [Register your agent](#registering-your-agent) so dispatch discovers it, then [write a CEL trigger](#writing-cel-triggers) to control when it runs. To iterate on your agent locally before registering, see [Testing locally](#testing-locally).

## How custom agents are dispatched

When you register a custom agent and give it a `trigger` expression, fullsend handles the rest — no per-agent workflow file required. Here is how an event reaches your agent on GitHub:

### The dispatch flow

1. **Event arrives.** A GitHub webhook fires (issue opened, label added, comment posted, PR submitted, etc.). The installed shim workflow forwards the event to the centralized dispatch workflow in `.fullsend/`.

2. **Normalize.** The `gha-event` input driver converts the raw GitHub event into a [`NormalizedEvent`](../../normative/normalized-event/v1/) — a forge-neutral struct with fields like `event.entity.kind`, `event.transition.kind`, and `event.actor.role`.

3. **Authorize.** `fullsend dispatch` enforces the platform authorization gate ([ADR 0054](../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)) before any agent is considered. Authorization is a platform-level decision — your CEL trigger does not need to implement permission checks (though you can add guards like `event.actor.role` if your agent has stricter requirements).

4. **Enumerate.** Dispatch loads all registered agents from the merged config (`agents:` list in org and per-repo `config.yaml`, plus scaffold discovery from [ADR 0058](../../ADRs/0058-agent-registration.md)). Each harness with a non-empty `trigger` field is a candidate.

5. **Evaluate.** Each candidate's CEL `trigger` expression is evaluated with `event` bound to the `NormalizedEvent`. Every harness whose trigger returns `true` is selected. Multiple agents can match the same event (parallel fan-out).

6. **Launch.** Matched agents are launched via `fullsend run` using the existing sandbox and execution infrastructure. The dispatch workflow passes the event payload, source repo, and any trigger-specific metadata to the agent workflow.

### What you configure vs. what dispatch handles

| You provide | Dispatch handles |
|---|---|
| Harness file with `trigger` expression | Normalizing the raw GitHub event |
| Agent definition (prompt, tools, model) | Authorizing the actor |
| Registration in `config.yaml` | Enumerating and evaluating all registered triggers |
| Pre/post scripts | Launching matched agents in the sandbox |

### Coexistence with built-in agents

Built-in agents (triage, code, review, fix, retro, prioritize) are routed by the dispatch workflow's stage-based routing logic. Custom agents with CEL triggers run alongside them — the two mechanisms coexist. A single event can trigger both a built-in agent via stage routing and one or more custom agents via CEL matching.

You can also keep a hand-written workflow that invokes `fullsend run` with a fixed harness path. CEL-based dispatch and explicit harness invocation may run side by side in the same installation.

## Writing CEL triggers

The harness `trigger` field is a [CEL](https://github.com/google/cel-spec) boolean expression evaluated against the incoming event. The expression has access to a single root variable, `event`, which is a [`NormalizedEvent`](../../normative/normalized-event/v1/) object.

A harness with no `trigger` field (or an empty trigger) is manual-only — it runs via `fullsend run` but is never selected by dispatch.

### NormalizedEvent fields

The `event` variable has the following top-level fields:

| Field | Type | Description |
|---|---|---|
| `event.repo` | string | Repository path (`owner/repo`) |
| `event.entity.kind` | string | `"work_item"` (issue) or `"change_proposal"` (PR) |
| `event.entity.id` | int | Issue or PR number |
| `event.transition.kind` | string | What happened — see [transition kinds](#transition-kinds) |
| `event.transition.label` | object | Present only when `kind == "label_changed"` |
| `event.transition.comment` | object | Present only when `kind == "comment_added"` |
| `event.transition.review` | object | Present only when `kind == "review_submitted"` |
| `event.actor.id` | string | Forge login of the user or bot that triggered the event |
| `event.actor.kind` | string | `"human"` or `"bot"` |
| `event.actor.role` | string | Repository permission: `admin`, `maintain`, `write`, `triage`, `read`, `none`, `external` |
| `event.state.labels` | list | Label names on the entity at event time |
| `event.state.change_proposal` | object | Present when a change proposal is involved (includes `is_fork`, `head_ref`, `base_ref`) |
| `event.source.system` | string | `"github"`, `"gitlab"`, `"jira"`, `"manual"`, or `"schedule"` |

### Transition kinds

| Kind | When it fires |
|---|---|
| `opened` | Issue or PR created |
| `reopened` | Issue or PR reopened after close |
| `edited` | Title/body/metadata edited (no new commits) |
| `synchronized` | PR head branch received new commits |
| `closed` | Issue or PR closed |
| `merged` | PR merged into target branch |
| `marked_ready` | Draft PR marked ready for review |
| `label_changed` | Label added or removed — check `event.transition.label.name` and `.action` (`"added"` or `"removed"`) |
| `comment_added` | Comment posted — check `event.transition.comment.command` for slash commands |
| `review_submitted` | PR review submitted — check `event.transition.review.state` (`"approved"`, `"changes_requested"`, `"commented"`, `"dismissed"`) |

### Common trigger patterns

**Run on new issues:**
```yaml
trigger: >
  event.entity.kind == "work_item"
    && event.transition.kind == "opened"
```

**Run when a specific label is added:**
```yaml
trigger: >
  event.transition.kind == "label_changed"
    && event.transition.label.name == "ready-for-my-agent"
    && event.transition.label.action == "added"
```

**Run on a slash command (on a PR, non-fork):**
```yaml
trigger: >
  event.transition.kind == "comment_added"
    && event.transition.comment.command == "/my-command"
    && event.entity.kind == "work_item"
    && event.state.change_proposal != null
    && !event.state.change_proposal.is_fork
```

**Run when a PR is opened or updated (non-fork):**
```yaml
trigger: >
  event.entity.kind == "change_proposal"
    && (event.transition.kind == "opened"
        || event.transition.kind == "synchronized"
        || event.transition.kind == "marked_ready")
    && !event.state.change_proposal.is_fork
```

**Run when review requests changes:**
```yaml
trigger: >
  event.transition.kind == "review_submitted"
    && event.transition.review.state == "changes_requested"
```

**Run only when the actor has write permission:**
```yaml
trigger: >
  event.entity.kind == "work_item"
    && event.transition.kind == "opened"
    && event.actor.role in ["admin", "maintain", "write"]
```

### Checking a label on the entity

Use `event.state.labels` to check labels on the issue or PR at event time:

```yaml
trigger: >
  event.entity.kind == "work_item"
    && event.transition.kind == "comment_added"
    && event.transition.comment.command == "/analyze"
    && "needs-analysis" in event.state.labels
```

### Fork safety

Write-capable agents that push commits or open PRs **must** guard against fork PRs. Use `!event.state.change_proposal.is_fork` in your trigger or rely on the platform authorization gate. Read-only agents (analysis, review) may run on fork PRs when policy allows.

### Verifying your trigger

Test your trigger expression locally before deploying:

```bash
# Validate CEL syntax (compiles the expression without evaluating)
fullsend trigger validate --expression 'event.entity.kind == "work_item" && event.transition.kind == "opened"'

# Evaluate against a NormalizedEvent fixture
fullsend trigger eval \
  --expression 'event.entity.kind == "work_item" && event.transition.kind == "opened"' \
  --input docs/normative/normalized-event/v1/examples/issue-opened.json
```

The [NormalizedEvent examples](../../normative/normalized-event/v1/examples/) directory contains fixtures for common GitHub events (issue opened, label added, PR opened, slash command, review submitted) that you can use for testing.

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
- **`forge.github`** scopes scripts and env vars to GitHub. When running on GitLab, a `forge.gitlab` block would take effect instead.
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
trigger: "event.type == 'issue'"    # Optional CEL expression over NormalizedEvent (see Writing CEL triggers)

# ── Composition ───────────────────────────────────────────────
base: harness/common-base.yaml      # Inherit from another harness (local or URL)

# ── Sandbox ───────────────────────────────────────────────────
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/base.yaml          # Sandbox policy (filesystem, landlock, process)
model: opus                         # LLM model override
readonly_repo: false                # Mount repo as read-only in sandbox
providers:                           # Network access via provider profiles (ADR 0065)
  - vertex-ai                       # References providers/vertex-ai.yaml
  - github                          # References providers/github.yaml

# ── Skills & plugins ──────────────────────────────────────────
skills:
  - skills/my-skill                  # Local path or URL with #sha256=...
plugins:
  - plugins/gopls-lsp
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
runner_env:                          # Legacy (same as env.runner)
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

# ── Forge-specific overrides ──────────────────────────────────
forge:
  github:
    pre_script: scripts/pre-gh.sh
    post_script: scripts/post-gh.sh
    skills: [skills/github-specific]  # Concatenated with top-level
    env:
      runner:
        GH_TOKEN: "${GH_TOKEN}"
  gitlab:
    pre_script: scripts/pre-gl.sh

# ── Security ──────────────────────────────────────────────────
security:
  fail_mode: closed                  # "closed" (default) or "open"
```

### Field merge rules (for `base` and `forge`)

| Field type | Behavior |
|-----------|----------|
| Scalars (`model`, `pre_script`, `image`, etc.) | Child wins if non-empty |
| `skills` | Merged with deduplication by basename (child overrides base) |
| `plugins`, `providers`, `api_servers`, `openshell.profiles` | Concatenated (base + child) |
| `host_files` | Concatenated; child overrides by `dest` |
| `env`, `runner_env` | Merged; child keys win |
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

Base chains support up to 5 levels (`MaxBaseDepth` in `internal/harness/compose.go`). Circular references are detected and rejected. Resolution order: base chain → child overrides → forge selection. See [field merge rules](#field-merge-rules-for-base-and-forge) for how each field type combines.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

## Configuring existing agents

You don't need to build from scratch to change how a built-in agent behaves. Use `base` to inherit the built-in harness and override just the fields you want — then register your configured version so it takes precedence.

### Example: add a skill to the code agent

Create a thin harness that inherits from the upstream code agent and adds your skill:

**`harness/code.yaml`:**
```yaml
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/code.yaml#sha256=abc...

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
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/review.yaml#sha256=abc...

model: sonnet
```

### Example: add org-specific environment variables

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/code.yaml#sha256=abc...

env:
  runner:
    JIRA_TOKEN: "${JIRA_TOKEN}"     # Merged with base env; child keys win
  sandbox:
    JIRA_PROJECT: "MYPROJ"
```

### What you can configure

Any harness field can be overridden. The [field merge rules](#field-merge-rules-for-base-and-forge) determine how your overrides combine with the base:

- **Change model, timeout, image, scripts** — scalars replace the base value.
- **Add skills** — your entries are merged with the base's by basename; same-named skills override the base entry. **Add plugins or host_files** — your entries are concatenated with the base's.
- **Add or override env vars** — maps are merged; your keys win on collision.
- **Replace validation or security config** — child replaces the entire block.

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

## Migrating from `customized/`

The `customized/` directory overlay ([ADR 0035](../../ADRs/0035-layered-content-resolution.md)) is deprecated in favor of the `base:` composition and config-driven registration described in this guide ([ADR 0064](../../ADRs/0064-deprecate-customized-directory-overlay.md)).

If you have existing files in `customized/`, the `fullsend agent migrate-customizations` command automates the conversion to config-driven agents.

Preview what would change:
```bash
fullsend agent migrate-customizations --fullsend-dir .fullsend --dry-run
```

Run the migration (creates a PR with the changes):
```bash
fullsend agent migrate-customizations --fullsend-dir .fullsend --repo owner/repo
```

The tool classifies each override and takes the appropriate action:

| Override type | Detection | Action |
|---------------|-----------|--------|
| Dead | Agent already registered in config | Delete `customized/` files |
| Custom | Not in upstream scaffold | Move files to regular directories, register local path in config |
| Modified | Standard scaffold agent, not yet in config | Generate a `base:` composition harness with the minimal diff, register in config |

For modified agents, the migration produces exactly the kind of thin `base:` harness shown in [Configuring existing agents](#configuring-existing-agents) — only the fields that differ from upstream are included.

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
| Agent not triggered by events | Verify your `trigger` expression — run `fullsend trigger validate` to check syntax, then `fullsend trigger eval` with a fixture to test matching |
| `allowed_remote_resources` error | URL agents require a matching prefix in `allowed_remote_resources` — `fullsend agent add` sets this automatically |
| `fullsend run` fails locally | Missing GCP credentials or sandbox image — see [Running agents locally](running-agents-locally.md) |
| Integrity hash mismatch | Remote content changed — run `fullsend agent update <name>` to re-pin |

## See also

- [fullsend-ai/agents](https://github.com/fullsend-ai/agents) — reference implementation used throughout this guide
- [NormalizedEvent v1 spec](../../normative/normalized-event/v1/) — full schema and examples for CEL trigger input
- [Customizing Agents with Skills](customizing-with-skills.md) — creating and managing skills
- [Customizing Agents with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Customizing agents](customizing-agents.md) — harness configurations and layered content resolution
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when configuration crosses into custom agent territory
- [Standalone mint](../infrastructure/standalone-mint.md) — custom agent roles and identity
