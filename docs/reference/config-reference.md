# Config Reference

Complete reference for all fields available in `.fullsend/config.yaml`. For
how these fields resolve through layered configuration, see
[Layered Config Reference](../guides/infrastructure/layered-config-reference.md).
Fleet manifests can declare the same managed-configuration schema as
`defaults.config` / per-repository `config` in `repos.yaml` (this is not
`config_base`, which is the configuration-preset path written to
`.fullsend/config.base.yaml`); see
[Repo Management — Managed configuration](../guides/getting-started/repo-management.md#managed-configuration).
For initial setup, see
[Configuring GitHub](../guides/getting-started/configuring-github.md) or
[Configuring GitLab](../guides/getting-started/configuring-gitlab.md).

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
allowed_remote_resources:            # URL prefixes allowed for remote resources (agents, policies, skills, plugins, profiles, providers, and base composition)
  - https://raw.githubusercontent.com/fullsend-ai/fullsend/
  - https://raw.githubusercontent.com/fullsend-ai/agents/

# ── Cross-repo issue creation ────────────────────────────────
create_issues:
  allow_targets:
    orgs:                            # GitHub orgs agents may create issues in
      - my-org
    repos:                           # Specific repos (owner/name) agents may create issues in
      - fullsend-ai/fullsend

# ── Authorization ────────────────────────────────────────────
authorization:                       # Extra sources of slash-command permission, checked before the collaborator API
  - provider: owners_file            # Grant access from the repo-root Prow OWNERS file (only provider)

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
  gateway:                           # Inference gateway credential route (ADR 0137)
    url: ""                          # Gateway origin, e.g. https://gateway.example.com (no path, port 443 only; plain http only for a loopback test host)
    audience: ""                     # OIDC audience the runner requests for the gateway (oidc mode)
    auth: ""                         # Credential mode: oidc (default) or api-key (ADR 0138)
    models: {}                       # Inline model list: id -> {api, compat, contextWindow, maxTokens}
    models_file: ""                  # Or: repository path to a pi-inference-gateway config file

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

URL prefixes allowed for remote resources in harness files — agent sources
(`agents:` entries with URLs), policies, skills, plugins, profiles, providers,
and `base:` composition. The config-level list acts as a fallback for all URL
resolution: a URL is accepted if it matches either the harness-level or the
config-level list. Default prefixes cover the fullsend and agents repositories.

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

### `authorization`

Lists extra sources of permission for triggering agents. The GitHub
collaborator API always applies; a provider listed here is checked first.
The only provider is `owners_file`:

- An `approvers` entry in the repo-root `OWNERS` file gets write-level access
  (every slash command and custom agent). A `reviewers` entry gets
  triage-level access, which covers `/fs-triage` and `/fs-review` only:
  custom agents under `agents:` still require write.
- An entry that names a key in `OWNERS_ALIASES` stands for that alias's
  members. A GitHub login equal to any alias key never matches, and nested
  aliases are not expanded.
- A user not found in `OWNERS` falls through to the collaborator API.
- If `OWNERS` or `OWNERS_ALIASES` cannot be parsed, or two alias keys differ
  only by case, the OWNERS check is skipped and only the collaborator API
  decides.
- Only the flat root `approvers`/`reviewers` lists are read. Prow `filters:`
  blocks and per-directory `OWNERS` files are ignored.

Default: absent (collaborator API only). The field is not inherited from
`config.base.yaml`: each repo opts in in its own `config.yaml`. Unknown or
duplicate providers fail config validation. See
[OWNERS file authorization](../guides/user/owners-file-authorization.md)
for a setup walkthrough.

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
  entirely. All three fields must come from one source for a run.
  `fullsend repos install` checks the same sources for repositories whose
  `inference.auth` is `openai-wif`. See
  [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md).
- **`gateway`** — a self-hosted, OpenAI/Anthropic-compatible inference
  gateway that validates the job's forge OIDC token directly
  ([ADR 0137](../ADRs/0137-inference-gateway-credential-route.md)). Models
  with the `gateway/` prefix use this route. The block applies when it is
  complete and the run has a forge OIDC endpoint (a GitHub Actions job with
  `id-token: write`). The runner then fetches the job's OIDC assertion for
  `audience` and puts it behind a run-scoped OpenShell provider with a
  per-host egress profile. It seeds the provider's placeholder into a
  runner-owned token file, and re-seeds it before each token's own `exp`.
  This happens in addition to every other provider: `openai/` and Vertex
  models in the same run keep their own routes. If the gateway cannot be
  reached, or refuses the token, the run fails and does not fall back to
  another credential. With no block, or on a local run without an OIDC
  endpoint, the runner adds nothing, so a harness that loads the
  inference-gateway extension as a plugin keeps working. A `gateway/` model
  on a runtime without the route (Claude Code, Codex) is an error. Fields:
  - `url` — the gateway origin, for example `https://gateway.example.com`.
    Must be `https` (plain `http` only for a loopback test host), with no
    credentials, query or fragment, no path other than `/`, and no port
    other than 443: the runner adds the `/v1/...` model API paths itself,
    and the egress profile allows the gateway host on port 443 only.
    The run route accepts a gateway on a private address, but
    [`fullsend inference gateway status`](../cli/inference.md#inference-gateway-status)
    refuses loopback, private and other internal addresses, so it cannot
    check one. For such a gateway, a run is the check: its first gateway
    model call fails if the gateway is unreachable or refuses the token.
  - `audience` — the OIDC audience the runner requests. The token is valid
    only at the gateway. Required in the `oidc` mode; not used in the
    `api-key` mode.
  - `auth` — the credential mode, `oidc` (the default) or `api-key`
    ([ADR 0138](../ADRs/0138-inference-gateway-api-key-credential-mode.md)).
    There is no precedence and no fallback between them. `oidc` is the
    route described above. `api-key` is for gateways that cannot trust
    forge OIDC. It is supported, but it is not the primary or safest
    option, because it relies on a long-lived secret: use it with caution,
    and prefer `oidc` when the gateway supports it. In the `api-key` mode
    the runner reads the gateway key from the
    `FULLSEND_INFERENCE_GATEWAY_API_KEY` forge secret or local environment
    variable, and fails the run when it is not set. It puts the key behind
    the same run-scoped provider placeholder, egress profile and guards as
    the OIDC token. The key is not rotated, so nothing is re-seeded. The
    block applies on local runs too, so the runner owns the route there
    as well.
  - `models` — optional inline model list, a map of model id to settings:
    `api` (one of `openai-responses`, `anthropic-messages`,
    `openai-completions`), and optional `compat`, `contextWindow` and
    `maxTokens`. `compat` holds pi request-feature flags: each value is a
    boolean, string or number, a flag that pi-inference-gateway v0.1.1
    knows must have that flag's type for the model's `api`, list-valued
    flags such as `allowedFallbackModels` are refused, and an unknown flag
    whose name looks like a credential or header is refused.
  - `models_file` — optional repository path, for example
    `.fullsend/inference-gateway.json`, to a file in the
    [pi-inference-gateway config format](https://github.com/fullsend-ai/pi-inference-gateway/blob/v0.1.1/docs/configuration.md#config-file).
    It must hold exactly one entry, `providers.gateway`, carrying only
    `models`, `include`, `exclude` and `defaultApi`, and every model must
    set its own `api`: pi runs offline, and the extension offers a
    configured model only when its entry names an `api`. Per-model values
    follow the extension's types: `compat` as for inline models,
    `contextWindow` and `maxTokens` positive whole numbers, `cost` only
    `input`, `output`, `cacheRead` and `cacheWrite` as non-negative
    numbers, and `thinkingLevelMap` thinking levels mapped to a string or
    `null`. Duplicate JSON keys are refused. These checks validate the
    file's shape; they are not secret detection. `fullsend github setup`
    commits the file as written, so keep credentials out of free-text
    values such as model names. `include`,
    `exclude` and `defaultApi` only apply to models discovered from the
    gateway, so they have no effect on a pi run. A file that sets
    `baseUrl`, `baseUrlEnv`, a credential key (`apiKey*`, `tokenFile`,
    `username*`, `password*`), `headers`, `authHeader`, `modelsPath`,
    `discovery` or `fallbackModels` is refused: the runner owns those.
    The runner reads the file from the repository checkout that
    `config.yaml` came from, so it is read at the same ref (the base
    branch on pull-request events), even when an org or managed layer set
    the path.

  `url` is always required, and in the `oidc` mode `url` and `audience`
  are all or none: `fullsend github setup` refuses to leave a block
  without them, and the runner fails a run whose resolved block is
  partial. `models` and `models_file` are mutually exclusive. A pi run on
  a `gateway/` model needs one of them, because pi runs offline and cannot
  discover the gateway's models. `url`, `audience` and `auth` layer
  independently; the model list (either form) is one unit,
  and a layer that sets it replaces the inherited list. There is no
  runner-variable override for this block. `fullsend github setup
  --inference-gateway-*` writes it and changes only the keys you pass, so
  a repository can add its models under a `url` and `audience` inherited
  from `config.base.yaml`.

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

## Installation model

This reference documents the **per-repo** config format (stored in
`.fullsend/config.yaml` within the target repository). Per-repo is the only
supported installation model. Per-org installation was removed
([ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md)). Fullsend
rejects a `config.yaml` that uses the old per-org format, which is any file
with top-level `dispatch`, `repos`, or `defaults` keys.

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
- [Configuring GitLab](../guides/getting-started/configuring-gitlab.md) —
  initial per-repo setup
- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — agent
  registration and harness authoring
- [Configuring Agent Behavior](../guides/user/customizing-agents.md) — tuning
  agents via config and harness
- [`fullsend agent`](../cli/agent.md) — CLI commands for managing registered
  agents
