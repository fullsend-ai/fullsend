# Config Reference

Complete reference for all fields available in `.fullsend/config.yaml`. For
how these fields resolve through layered configuration, see
[Layered Config Reference](../guides/infrastructure/layered-config-reference.md).
For initial setup, see
[Configuring GitHub](../guides/getting-started/configuring-github.md).

```yaml
# ── Schema ───────────────────────────────────────────────────
version: "1"                         # Schema version (required)

# ── Platform ─────────────────────────────────────────────────
forge: github                        # Hosting platform: github, gitlab (auto-detected if omitted)
tracker: jira                        # Default issue tracker for `fullsend issues` commands: github, gitlab, jira

# ── Operations ───────────────────────────────────────────────
kill_switch: false                   # Emergency stop — disables all agent dispatch when true
keep_history: true                   # Append previous sticky-comment content as collapsed "Previous run" blocks

# ── Runtime ──────────────────────────────────────────────────
runtime: claude                      # Default agent runtime: claude, pi, codex

# ── Roles ────────────────────────────────────────────────────
roles:                               # Agent roles to install (determines which Apps and credentials are provisioned)
  - triage
  - coder
  - review
  - fix
  - retro
  - prioritize

# ── Agents ───────────────────────────────────────────────────
agents:                              # Registered agent sources and per-agent tuning
  - source: https://example.com/triage.yaml#sha256=abc...   # URL (requires #sha256=) or local path
    name: triage                     # Explicit name (derived from source filename if omitted)
    ref: main                        # Branch/tag resolved at adoption; used by `agent update`
    enabled: true                    # Toggle agent on/off without removing the entry
    runtime: claude                  # Per-agent runtime override
    model: opus                      # Per-agent model override (model id or provider/id)
    effort: high                     # Per-agent reasoning effort: low, medium, high, xhigh, max
    subagents:                       # Per-agent sub-agent model map
      default: sonnet                # Fallback model for personas without an explicit entry
      code-reviewer: opus            # Model for a specific persona

# ── Remote resources ─────────────────────────────────────────
allowed_remote_resources:            # URL prefixes allowed for remote agent sources and base composition
  - https://raw.githubusercontent.com/fullsend-ai/fullsend/
  - https://raw.githubusercontent.com/fullsend-ai/agents/

# ── Cross-repo issue creation ────────────────────────────────
create_issues:
  allow_targets:
    orgs:                            # GitHub orgs agents may create issues in
      - my-org
    repos:                           # Specific repos (owner/name) agents may create issues in
      - fullsend-ai/fullsend

# ── Status notifications ─────────────────────────────────────
status_notifications:
  comment:
    start: enabled                   # Post a comment when an agent starts: enabled (default), disabled
    completion: enabled              # Post a comment when an agent completes: enabled (default), on_failure, disabled
  reaction:
    start: disabled                  # Add an emoji reaction on start: enabled, disabled (default)
    completion: disabled             # Add an emoji reaction on completion: enabled, on_failure, disabled (default)

# ── Mint ─────────────────────────────────────────────────────
mint_url: https://mint.fullsend.sh   # Token mint URL for credential issuance

# ── Inference ────────────────────────────────────────────────
inference:
  provider: vertex                   # Inference backend: vertex
  project: my-gcp-project           # GCP project ID (must be provided by installer)
  region: global                     # GCP region for inference requests
  wif_provider: projects/123/locations/global/workloadIdentityPools/pool/providers/prov  # WIF provider resource name
  openai:                            # OpenAI Workload Identity Federation (ADR 0092)
    audience: ""                     # OpenAI WIF audience
    identity_provider_id: ""         # OpenAI WIF identity provider ID
    service_account_id: ""           # OpenAI WIF service account ID

# ── Model aliases ────────────────────────────────────────────
models:
  aliases:                           # Override fullsend's pinned model alias table per key
    opus: claude-opus-4-6            # Model id or provider/id spec
    sonnet: claude-sonnet-5
    haiku: claude-haiku-3-5
    fable: claude-fable-5-1
```

## Field details

Most fields are self-explanatory from the inline comments above. This section
expands on fields where additional context helps.

### `version`

Schema version string. Must be `"1"`. Present in every config file.

### `forge`

Hosting platform for the repository (`github` or `gitlab`). When omitted,
fullsend auto-detects the forge from CI environment variables. Most
installations do not need to set this explicitly.

### `tracker`

Default issue tracker for `fullsend issues` commands' `--tracker` flag
(`github`, `gitlab`, or `jira`). Distinct from `forge` — a repository hosted
on GitHub may track issues in Jira. When omitted, `--tracker` is required on
every `fullsend issues` invocation. See
[Jira Integration](../guides/user/jira-integration.md) for cross-platform
setup.

### `kill_switch`

Emergency stop. When set to `true`, all agent dispatch is disabled for the
repository. Uses pointer semantics in the layered config system — `nil`
(omitted) falls through to parent; an explicit `false` is a local decision
that does not fall through.

### `keep_history`

Controls whether sticky comment updates (from post-review, post-comment, and
issues post-comment) append the previous comment body as a collapsed "Previous
run" `<details>` block. Default is `true` (history appended). Set to `false`
when accumulated "Previous run" blocks add unwanted noise — for example, when
comments are synced to Jira where `<details>` does not render as collapsible.

### `runtime`

Default agent runtime for all agents in this repository. Valid values:
`claude`, `pi`, `codex`. Per-agent overrides in the `agents:` list take
precedence. Per-run overrides via `--runtime` flag or `FULLSEND_RUNTIME` env
var take precedence over both. See
[Choose a Runtime](../guides/getting-started/choosing-a-runtime.md) for
runtime comparison.

### `roles`

Agent roles determine which GitHub Apps (and their associated credentials and
permissions) are provisioned for the repository. Valid roles: `fullsend`,
`triage`, `coder`, `review`, `fix`, `retro`, `prioritize`, `e2e`.

In the layered config system, `roles` uses replace-if-set semantics — an
overlay that sets `roles` replaces the parent list entirely (no union).
Omitting the key inherits the parent's roles. See
[Layered Config Reference](../guides/infrastructure/layered-config-reference.md).

### `agents`

Registered agent sources and per-agent tuning. Each entry identifies an agent
by source URL or local path, with optional overrides for runtime, model,
effort, and sub-agent models.

- **`source`** — URL (must include `#sha256=<64-hex-char>` integrity hash) or
  local path. URL sources must be covered by `allowed_remote_resources`.
- **`name`** — Explicit name. When omitted, derived from the source filename
  (e.g., `triage.yaml` → `triage`). Must start with an alphanumeric character
  and contain only `[a-zA-Z0-9_-]`.
- **`ref`** — Branch or tag resolved when the agent was adopted via
  `fullsend agent add`. When present, `fullsend agent update` re-resolves
  against this ref. Empty for SHA-pinned or legacy entries.
- **`enabled`** — Toggle the agent without removing its entry. A
  suppression-only entry (`enabled: false`, no source) disables a built-in or
  parent-layer agent by name.
- **`runtime`**, **`model`**, **`effort`** — Per-agent overrides. An
  override-only entry (no source, at least one setting) tunes a built-in
  agent by name.
- **`subagents`** — Map of persona names to model references. The `default`
  key sets the fallback for personas without an explicit entry. A `~` (null)
  value tombstones an inherited entry. Keys must be lowercase alphanumeric
  segments joined by hyphens (max 64 chars).

In the layered config system, agents use keyed merge by `DerivedName()` — see
[Layered Config Reference](../guides/infrastructure/layered-config-reference.md).

For agent registration and management, see
[`fullsend agent`](../cli/agent.md) and
[Bring Your Own Agent](../guides/user/bring-your-own-agent.md).

### `allowed_remote_resources`

URL prefixes allowed for remote agent sources (`agents:` entries with URLs)
and `base:` composition in harness files. Default prefixes cover the fullsend
and agents repositories.

In the layered config system, this field uses union-with-deny-all semantics:
omitted inherits from parent; explicit empty (`[]`) denies all remote
resources; a non-empty list is unioned with the parent's list. See
[Layered Config Reference](../guides/infrastructure/layered-config-reference.md).

### `create_issues`

Controls cross-repo issue creation by agents. The `allow_targets` field
restricts which orgs and repos agents may create issues in.

- **`allow_targets.orgs`** — GitHub organizations. All repos in these orgs are
  allowed.
- **`allow_targets.repos`** — Specific repos in `owner/name` format.

When omitted, agents cannot create issues outside the repository they are
running in.

### `status_notifications`

Controls the comments and reactions fullsend posts on issues and PRs when
agents start and complete.

- **`comment.start`** — `enabled` (default) or `disabled`.
- **`comment.completion`** — `enabled` (default), `on_failure` (comment only
  on failure), or `disabled`.
- **`reaction.start`** — `enabled` or `disabled` (default). Reactions are
  an opt-in alternative that does not generate a GitHub notification.
- **`reaction.completion`** — `enabled`, `on_failure`, or `disabled`
  (default).

### `mint_url`

Token mint URL for credential issuance. The mint service issues short-lived
credentials to agents based on their role. Default:
`https://mint.fullsend.sh` (the hosted public mint). Custom mint deployments
set this to their own URL. See
[Standalone Mint](../guides/infrastructure/standalone-mint.md) and
[Mint Administration](../guides/infrastructure/mint-administration.md).

### `inference`

Groups inference backend settings under a single key. Each subfield resolves
independently through the layered config system (an overlay can override
`project` without restating `provider`).

- **`provider`** — Inference backend identifier. Currently only `vertex`.
  Default: `vertex`.
- **`project`** — GCP project ID for inference requests. No default — must be
  provided by the installer or an existing secret.
- **`region`** — GCP region for inference. Default: `global`.
- **`wif_provider`** — Full Workload Identity Federation provider resource
  name. No default — must be provided by the installer.
- **`openai`** — OpenAI Workload Identity Federation identifiers
  ([ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)). The
  `FULLSEND_OPENAI_*` runner variables, when set, replace the resolved block
  entirely. All three fields must come from one source for a run. See
  [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md).

For setup instructions, see
[Getting Inference](../guides/getting-started/getting-inference.md).

### `models`

Model configuration, currently containing only `aliases`.

- **`aliases`** — Per-key overrides of fullsend's pinned model alias table.
  Keys are alias names (`opus`, `sonnet`, `haiku`, `fable`); values are model
  ids or `provider/id` specs (e.g., `claude-opus-4-6`,
  `google-vertex/gemini-3.8-flash`). An unknown key is a validation error. A
  value must not be another alias name — aliases resolve once, so `sonnet:
  opus` would reach the provider as the literal id "opus". In the layered
  config system, aliases merge per key across layers. See
  [Layered Config Reference](../guides/infrastructure/layered-config-reference.md).

## Per-repo vs. org-mode

This reference documents the **per-repo** config format (stored in
`.fullsend/config.yaml` within the target repository). Per-repo is the sole
supported installation model going forward — per-org installation mode is
deprecated ([ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md))
and installations still on org mode should migrate.

## Layered configuration

Per-repo config supports a two-file layered system:

| File | Role |
|------|------|
| `config.yaml` | Overlay — repo-specific customization (writable) |
| `config.base.yaml` | Base — vendor preset or shared baseline (read-through) |

Fields unset in the overlay fall through to the base layer, then to compiled-in
code defaults. For complete merge rules, see
[Layered Config Reference](../guides/infrastructure/layered-config-reference.md).

## See also

- [Layered Config Reference](../guides/infrastructure/layered-config-reference.md)
  — precedence, merge rules, and code defaults for every field
- [Harness Field Reference](harness-reference.md) — fields available in harness
  YAML files (per-agent configuration)
- [Configuring GitHub](../guides/getting-started/configuring-github.md) —
  initial per-repo setup
- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — agent
  registration and harness authoring
- [Configuring Agent Behavior](../guides/user/customizing-agents.md) — tuning
  agents via config and harness
- [`fullsend agent`](../cli/agent.md) — CLI commands for managing registered
  agents
