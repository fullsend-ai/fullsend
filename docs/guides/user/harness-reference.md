# Harness Field Reference

Complete reference for all fields available in a fullsend harness YAML file. For a guide-oriented introduction to harnesses, see [Bring Your Own Agent](bring-your-own-agent.md).

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

# ── Forge-specific overrides ──────────────────────────────────
forge:
  github:
    pre_script: scripts/pre-gh.sh
    post_script: scripts/post-gh.sh
    skills: [skills/github-specific]  # Concatenated with top-level
    providers: [providers/github.yaml] # Concatenated with top-level
    openshell:
      profiles: [profiles/github.yaml] # Concatenated with top-level
    host_files:                        # Forge-specific host files
      - src: env/github.env
        dest: /run/secrets/forge.env
    env:
      runner:
        GH_TOKEN: "${GH_TOKEN}"
  gitlab:
    pre_script: scripts/pre-gl.sh

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

## Deprecated fields

> **Deprecated:** `runner_env` is deprecated. Use `env.runner`
> instead. The `runner_env` field still works but emits a deprecation warning
> at runtime. Migration: move `runner_env:` entries under `env: runner:` and
> delete the `runner_env:` block.

## Field merge rules (for `base` and `forge`)

| Field type | Behavior |
|-----------|----------|
| Scalars (`model`, `pre_script`, `policy`, `image`, etc.) | Child wins if non-empty |
| `skills` | Merged with deduplication by basename (child overrides base) |
| `providers`, `openshell.profiles` | Concatenated (base + child); also applies per-forge |
| `plugins`, `api_servers` | Concatenated (base + child) |
| `host_files` | Concatenated; child overrides by `dest` |
| `env`, `runner_env` (deprecated) | Merged; child keys win |
| `validation_loop`, `security` | Child replaces entirely |
| `allowed_remote_resources`, `allow_runtime_fetch`, `max_runtime_fetches` | NOT inherited (child must declare its own) |

## Referencing resources: local vs. remote

**Local paths** resolve relative to the harness file's base directory:
```yaml
agent: agents/triage.md              # → {base}/agents/triage.md
```

**Remote URLs** require a `#sha256=...` integrity hash:
```yaml
agent: https://raw.githubusercontent.com/org/repo/<sha>/agents/lint.md#sha256=abc...
```

**Scripts are local-only** — `pre_script`, `post_script`, and `validation_loop.script` must be local paths (they run on the trusted runner). Exception: scripts declared in a `base` harness fetched via URL are allowed.

## See also

- [Bring Your Own Agent](bring-your-own-agent.md) — end-to-end guide for building and registering agents
- [Configuring agent behavior](customizing-agents.md) — harness configurations and `base:` composition
- [CEL Triggers Reference](cel-triggers-reference.md) — dispatch flow and trigger patterns
