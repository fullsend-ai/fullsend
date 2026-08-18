# Harness Reference

Complete reference for harness YAML fields, merge rules, and advanced
configuration. For an introduction to harness configuration and `base:`
composition, see [Configuring Agent Behavior](customizing-agents.md).

## Harness field reference

```yaml
# -- Required ---------------------------------------------------------------
agent: agents/my-agent.md           # Path to agent definition
role: my-agent                      # Role name (lowercase letter first, then a-z, 0-9, _, -; no double hyphens)

# -- Identity & metadata ----------------------------------------------------
slug: my-org-my-role                # GitHub App identity (convention: <org>-<role>)
description: One-line summary       # Human-readable description
doc: docs/agents/my-agent.md        # Source-repo-only; not resolved at runtime
trigger: "event.entity.kind == 'work_item'"  # Optional CEL expression over NormalizedEvent (see CEL Triggers Reference)

# -- Composition -------------------------------------------------------------
base: harness/common-base.yaml      # Inherit from another harness (local or URL)

# -- Sandbox -----------------------------------------------------------------
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/base.yaml          # Sandbox policy (filesystem, landlock, process)
model: opus                         # LLM model override
effort: high                        # Reasoning effort (low, medium, high, xhigh, max); claude runtime only
readonly_repo: false                # Mount repo as read-only in sandbox
providers:                           # Network access via provider profiles
  - vertex-ai                       # References providers/vertex-ai.yaml
  - github                          # References providers/github.yaml

# -- Skills & plugins --------------------------------------------------------
skills:
  - skills/my-skill                  # Local path or URL with #sha256=...
plugins:
  - plugins/gopls-lsp                # Local path or URL with #sha256=...
openshell:                           # OpenShell sandbox profiles
  profiles:
    - https://example.com/profile.yaml#sha256=abc...

# -- Scripts (local paths only) -----------------------------------------------
pre_script: scripts/pre-my-agent.sh
post_script: scripts/post-my-agent.sh
agent_input: inputs/my-input.md     # File passed as initial input to the agent

# -- Validation ---------------------------------------------------------------
validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2
  feedback_mode: stderr              # How validation feedback reaches the agent

# -- Host files ---------------------------------------------------------------
host_files:
  - src: env/my-agent.env            # Runner path (supports ${VAR})
    dest: /sandbox/workspace/.env.d/my-agent.env
    expand: true                     # Resolve ${VAR} in contents
  - src: ${SOME_CREDENTIAL}
    dest: /tmp/.cred.json
    optional: true                   # Skip if missing

# -- Environment ---------------------------------------------------------------
env:
  runner:                            # Available to pre/post scripts
    MY_VAR: "${MY_VAR}"
  sandbox:                           # Available inside sandbox
    MY_SETTING: "value"
runner_env:                          # Deprecated: use env.runner instead
  MY_VAR: "${MY_VAR}"

# -- Timeouts -----------------------------------------------------------------
timeout_minutes: 20
sandbox_timeout_seconds: 300         # 30-600

# -- Remote resources ---------------------------------------------------------
allowed_remote_resources:
  - https://github.com/my-org/agent-library/
allow_runtime_fetch: true
max_runtime_fetches: 10

# -- API servers ---------------------------------------------------------------
api_servers:                         # Host-side REST proxies exposed to sandbox
  - name: my-api
    script: scripts/api-server.sh    # Local script that runs the server
    port: 8080                       # Port the sandbox connects to
    env:                             # Env vars for the server process
      API_KEY: "${API_KEY}"

# -- Forge-specific overrides --------------------------------------------------
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

# -- Security ------------------------------------------------------------------
security:
  enabled: true                  # All scanners enabled by default
  fail_mode: closed              # "closed" (default) or "open"
  host_scanners:
    unicode_normalizer: true
    context_injection: true
    ssrf_validator: true
    secret_redactor: true
    llm_guard:
      enabled: true
      threshold: 0.92
      match_type: sentence
  sandbox_hooks:
    tirith:
      enabled: true
      fail_on: high              # "critical", "high", or "medium"
    ssrf_pretool: true
    secret_redact_posttool: true
    unicode_posttool: true
    context_suppress_posttool: true
    canary_pretool: true
    canary_posttool: true
  escalation:
    on_critical: halt            # "halt" or "review"
    review_label: requires-manual-review
  trace:
    enabled: true
```

> **Naming convention:** Prefix settings that tune one agent's behavior with
> that agent's role in caps, e.g. `REVIEW_SEVERITY_THRESHOLD` — this avoids
> collisions when multiple agents share a sandbox or env file.
>
> A setting meant to apply the same way across every agent (like
> `roles` or `create_issues.allow_targets`) belongs in `config.yaml`
> instead, not as an env var.

### Deprecated fields

> **Deprecated:** `runner_env` is deprecated. Use `env.runner`
> instead. The `runner_env` field still works but emits a deprecation warning
> at runtime. Migration: move `runner_env:` entries under `env: runner:` and
> delete the `runner_env:` block.

## Field merge rules

These rules apply when using `base:` composition and `forge:` overrides.

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
agent: agents/triage.md              # -> {base}/agents/triage.md
```

**Remote URLs** require a `#sha256=...` integrity hash:
```yaml
agent: https://raw.githubusercontent.com/org/repo/<sha>/agents/lint.md#sha256=abc...
```

**Scripts are local-only** — `pre_script`, `post_script`, and `validation_loop.script` must be local paths (they run on the trusted runner). Exception: scripts declared in a `base` harness fetched via URL are allowed.

## Remote providers and profiles

Providers and openshell profiles can be referenced from remote URLs, enabling fully portable harnesses that bundle everything an agent needs.

**`providers`** accepts local provider names, local file paths, and HTTPS URLs with integrity hashes:

```yaml
providers:
  - vertex                       # Local name: loaded from providers/vertex.yaml
  - providers/custom.yaml        # Local path: resolved relative to harness
  - "https://github.com/org/repo/tree/main/providers/claude.yaml#sha256=abc..."  # Remote
```

**`openshell.profiles`** accepts local paths and HTTPS URLs:

```yaml
openshell:
  profiles:
    - profiles/claude-code.yaml    # Local path (resolved relative to harness)
    - "https://github.com/org/profiles/tree/main/claude-code.yaml#sha256=abc..."
```

When using `base:` composition, the base harness can declare its own providers and profiles. Child harnesses inherit and can extend them:

- **Profiles:** base + child lists are concatenated; deduplicated by profile `id` (child wins)
- **Providers:** base + child lists are concatenated; local names shadow URL-resolved names of the same `name`

Remote URLs must include a `#sha256=...` integrity hash and match an `allowed_remote_resources` prefix in the same config. The integrity hash is checked on every resolution to ensure the content hasn't been tampered with since it was pinned.

## Pre-commit tool dependencies

Fullsend auto-detects and installs tools required by a target repo's pre-commit hooks. The resolver reads `.pre-commit-config.yaml`, matches hooks against a tools registry, and installs missing dependencies before the authoritative pre-commit check runs.

Only hooks that pre-commit **cannot self-serve** need registry entries:
- `language: system` — the tool must already be on `PATH`
- `language: golang` — binary download is faster than Go compilation

Hooks using `language: python`, `language: node`, or `language: docker_image` are handled natively by pre-commit and need no registry entry.

### Two-layer resolution

```
upstream defaults (fullsend-ai/agents)
  -> per-repo additive:  .pre-commit-tools.yaml at repo root
```

| Layer | Location | Behavior |
|-------|----------|----------|
| Upstream | Provided at runtime by the agents repo | Base registry shipped with fullsend |
| Per-repo additive | `.pre-commit-tools.yaml` at target repo root | **Merges** with upstream registry |

**Per-repo additive merge** is designed for repos that need to extend the registry with one or two entries. New entries are appended, entries matching an existing `(repo, hook_id)` key override it, and entries with `exclude: true` suppress the matching upstream entry.

### Adding a custom binary tool

1. Create a `.pre-commit-tools.yaml` file at your repo root.
2. Add an entry with the `hook_id`, `repo`, and `install` fields:

    ```yaml
    tools:
      - hook_id: my-linter
        repo: https://github.com/example/my-linter
        install:
          type: binary
          name: my-linter
          version: "1.2.3"
          url_template: "https://github.com/example/my-linter/releases/download/v{version}/my-linter-{triple}.tar.gz"
          checksums:
            x86_64: "abc123..."
            aarch64: "def456..."
          binary_name: my-linter
    ```

3. Commit and merge to the base branch. The entry is merged with the upstream registry — all upstream tools remain available.

### Suppressing an upstream entry

1. Add an entry to `.pre-commit-tools.yaml` with the matching `hook_id` and `repo`, plus `exclude: true`:

    ```yaml
    tools:
      - hook_id: gitleaks
        repo: https://github.com/zricethezav/gitleaks
        exclude: true
    ```

2. Commit and merge to the base branch. The upstream tool will no longer be installed for this repo.

### Security

Per-repo registries are read from the **base branch**, not from the PR's working tree. This means changes to `.pre-commit-tools.yaml` in a PR do not take effect until the PR is merged. This is intentional — the tool installation pipeline runs outside the sandbox with elevated permissions, and PR content is untrusted.

## See also

- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [Configuring Agent Behavior](customizing-agents.md) — harness composition and agent configuration
- [Bring Your Own Agent](bring-your-own-agent.md) — building and registering custom agents
- [CEL Triggers Reference](cel-triggers-reference.md) — trigger expressions for agent dispatch
