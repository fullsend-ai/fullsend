# Configuring Agent Behavior

This guide explains how to configure fullsend agents for your organization and repositories through harness configurations and layered content resolution.

## Harness Configuration

Each agent run is configured by a harness YAML file that defines the complete execution environment. These files live in the `.fullsend` config repo (per-org mode) or `.fullsend/customized/harness/` (per-repo mode).

### Harness YAML Structure

A minimal harness configuration (based on actual fullsend agent harnesses):

```yaml
agent: agents/code.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-code:latest
policy: policies/base.yaml
providers:
  - vertex-ai
  - github
  - package-registries
timeout_minutes: 35

skills:
  - skills/code-implementation

plugins:
  - plugins/gopls-lsp

host_files:
  - src: env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /sandbox/workspace/.gcp-oidc-token
    optional: true

pre_script: scripts/pre-code.sh
post_script: scripts/post-code.sh

validation_loop:
  script: scripts/validate-output-schema.sh
  schema: schemas/result.schema.json
  max_iterations: 2

env:
  runner:
    PUSH_TOKEN: "${PUSH_TOKEN}"  # auto-minted in CI when --mint-url is provided
    REPO_FULL_NAME: "${REPO_FULL_NAME}"
    REPO_DIR: "${GITHUB_WORKSPACE}/target-repo"
```

> **Where relative script paths resolve.** A **local** harness — the case for
> the example above — resolves `pre_script` / `post_script` against your
> workspace root, so `scripts/pre-code.sh` means *your* `scripts/` directory
> and the harness must ship its own copy. A **URL-sourced** harness resolves
> them against the parent of the harness file's directory in the source repo;
> for the stock agents that is [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents),
> which owns `scripts/pre-code.sh` and `scripts/post-code.sh`.
>
> The fullsend scaffold no longer ships `scripts/pre-code.sh` or
> `scripts/pre-fix.sh`, so copying a stock agents harness into a local file
> without also copying its scripts will fail validation. Reference the
> agents-repo harness by URL instead, or supply your own scripts.
>
> A `pre_script` can also stop the run before the sandbox starts — see the
> [pre-script output protocol](../../normative/prescript-output/v1/README.md).

**Optional fields** (all have secure defaults and can be omitted):

```yaml
providers:                       # Inference providers (names, local paths, or URLs)
  - vertex                       # Local name: references providers/vertex.yaml
  - providers/custom.yaml        # Local path: resolved relative to harness
  - "https://github.com/org/repo/tree/main/providers/claude.yaml#sha256=abc..."  # Remote URL

openshell:                       # Openshell provider profiles (local paths or URLs)
  profiles:
    - profiles/claude-code.yaml    # Local path: resolved relative to harness
    - "https://github.com/org/profiles/tree/main/claude-code.yaml#sha256=def..."

validation_loop:                     # script is required; these sub-fields are optional
  script: scripts/validate-output-schema.sh
  schema: schemas/result.schema.json  # JSON Schema file for output validation (optional)
  feedback_mode: stderr          # "stderr", "stdout", or "exit_code" (optional)

allowed_remote_resources:        # URL prefixes allowed for remote skills/agents/plugins/policies
  - https://github.com/org/       # Omit field: first-party defaults apply automatically
                                   # Non-empty list: your entries + first-party defaults appended
                                   # Set to [] to deny all remote fetches (deny-all)
allow_runtime_fetch: true         # Opt-in to runtime skill fetching (default: false)
max_runtime_fetches: 10           # Max runtime fetch requests per run (1–1000, default: 10)

security:                        # Security is enabled by default with fail_mode: closed
  enabled: true                  # All scanners enabled by default
  fail_mode: closed              # "closed" (reject on failure) or "open" (warn only)
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

### Remote Providers and Profiles

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

## Layered Configuration Resolution

> **Deprecated:** The `customized/` directory overlay mechanism described
> below is deprecated. Use `base:` composition instead: register agents in
> `config.yaml` with a `base:` URL pointing to the upstream harness, and
> override only the fields that differ. See
> [Bring Your Own Agent](bring-your-own-agent.md) for the composition model
> and config-driven registration.

Fullsend uses a three-tier configuration inheritance model for all configuration: agent definitions, skills, policies, harness definitions, and guardrails. Each configuration tier can extend or override the one below it.

```
┌──────────────────────────────────────────────────────────────┐
│    Configuration Layering (deprecated; use base: instead)    │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  Priority (highest wins):                                    │
│                                                              │
│  ┌──────────────────────┐                                    │
│  │ Per-repo overrides   │  .fullsend/customized/{dir}/       │
│  │ (target repo)        │  in the target repository          │
│  └──────────┬───────────┘                                    │
│             │ overrides                                      │
│  ┌──────────▼───────────┐                                    │
│  │ Org-level overrides  │  customized/{dir}/                 │
│  │ (.fullsend config    │  in the .fullsend config repo      │
│  │  repo)               │                                    │
│  └──────────┬───────────┘                                    │
│             │ overrides                                      │
│  ┌──────────▼───────────┐                                    │
│  │ Upstream defaults    │  Provided at runtime by reusable   │
│  │ (fullsend-ai/        │  workflow workspace preparation    │
│  │  fullsend)           │                                    │
│  └──────────────────────┘                                    │
│                                                              │
│  Layered directories:                                        │
│    agents/  skills/  schemas/  harness/  plugins/            │
│    policies/  scripts/  env/                                 │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### The `customized/` Directory Structure

Orgs configure layered directories by placing overrides in `customized/` subdirectories:

```
.fullsend/                          (config repo)
├── customized/
│   ├── agents/                     → Override agent definitions
│   ├── skills/                     → Add/override skills
│   ├── schemas/                    → Override output schemas
│   ├── harness/                    → Override harness configs
│   ├── policies/                   → Override security policies
│   ├── scripts/                    → Override hook scripts
│   └── env/                        → Override environment files
└── .github/workflows/              → Reusable workflows
```

For per-repo mode, the same structure lives at `.fullsend/customized/` within the target repo.

### How Override Resolution Works

> **Deprecated:** This section describes the deprecated `customized/` overlay.
> See the [deprecation notice above](#layered-configuration-resolution).

**File-level replacement, not field-level merging.** When you place a file in `customized/harness/code.yaml`, it completely replaces the upstream `harness/code.yaml`. There is no YAML field merging.

**Example: Adding a skill to the code agent**

To add a custom skill to the code agent's harness (deprecated — use `base:` composition instead):

1. **Copy the full upstream harness** from `fullsend-ai/fullsend` to your customization directory:
   ```bash
   # ⚠ Deprecated: use config-driven agent registration instead
   curl -o .fullsend/customized/harness/code.yaml \
     https://raw.githubusercontent.com/fullsend-ai/agents/main/harness/code.yaml
   ```

2. **Edit the copy** to add your skill:
   ```yaml
   skills:
     - skills/code-implementation      # Upstream skill
     - skills/my-custom-validation      # Your custom skill
   ```

3. **Add your custom skill directory**:
   ```bash
   # Create your custom skill
   mkdir -p .fullsend/customized/skills/my-custom-validation
   cat > .fullsend/customized/skills/my-custom-validation/SKILL.md <<'EOF'
   # My Custom Validation Skill

   [Your skill content...]
   EOF
   ```

**At runtime**, the reusable workflow:
- Copies upstream defaults to `harness/`, `skills/`, etc.
- Copies your `customized/` files on top, **replacing** any files with matching names
- The harness loads `harness/code.yaml` (now your customized version)
- Your skill at `skills/my-custom-validation/` is available

**Important:** You must maintain the full harness structure. You cannot add just a `skills:` field—the entire YAML file must be present and valid.

### Configuring Pre-commit Tool Dependencies

> **Note:** The `customized/scripts/.pre-commit-tools.yaml` L1 overlay path
> referenced below uses the deprecated `customized/` mechanism.
> The per-repo L2 path (`.pre-commit-tools.yaml` at repo root) is unaffected.

Fullsend auto-detects and installs tools required by a target repo's pre-commit hooks. The resolver reads `.pre-commit-config.yaml`, matches hooks against a tools registry, and installs missing dependencies before the authoritative pre-commit check runs.

Only hooks that pre-commit **cannot self-serve** need registry entries:
- `language: system` — the tool must already be on `PATH`
- `language: golang` — binary download is faster than Go compilation

Hooks using `language: python`, `language: node`, or `language: docker_image` are handled natively by pre-commit and need no registry entry.

#### Prerequisites

- A `.pre-commit-config.yaml` in the target repo with hooks that use `language: system` or `language: golang`.
- Access to commit to the target repo's base branch (L2 per-repo registries only take effect after merge).

#### Three-layer resolution

```
upstream defaults (fullsend-ai/fullsend)
  → org replacement:  customized/scripts/.pre-commit-tools.yaml  (L1)
    → per-repo additive:  .pre-commit-tools.yaml at repo root    (L2)
```

| Layer | Location | Behavior |
|-------|----------|----------|
| Upstream | Provided at runtime by reusable workflow | Base registry shipped with fullsend |
| L1 org replacement | `customized/scripts/.pre-commit-tools.yaml` in `.fullsend` config repo | **Completely replaces** upstream registry |
| L2 per-repo additive | `.pre-commit-tools.yaml` at target repo root | **Merges** with upstream/org registry |

**L1 replacement** works via the layered overlay — the file is copied over the upstream registry at runtime. Use this when your org needs a completely different set of tools.

**L2 additive merge** is designed for repos that need to extend the registry with one or two entries. New entries are appended, entries matching an existing `(repo, hook_id)` key override it, and entries with `exclude: true` suppress the matching upstream entry.

> **Note:** There are two per-repo customization paths with different semantics:
> - `.fullsend/customized/scripts/.pre-commit-tools.yaml` — L1 full replacement (same overlay mechanism as other layered dirs)
> - `.pre-commit-tools.yaml` at repo root — L2 additive merge (resolver discovers and merges)

#### Adding a custom binary tool

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

#### Suppressing an upstream entry

1. Add an entry to `.pre-commit-tools.yaml` with the matching `hook_id` and `repo`, plus `exclude: true`:

    ```yaml
    tools:
      - hook_id: gitleaks
        repo: https://github.com/zricethezav/gitleaks
        exclude: true
    ```

2. Commit and merge to the base branch. The upstream tool will no longer be installed for this repo.

#### Security

Per-repo registries are read from the **base branch**, not from the PR's working tree. This means changes to `.pre-commit-tools.yaml` in a PR do not take effect until the PR is merged. This is intentional — the tool installation pipeline runs outside the sandbox with elevated permissions, and PR content is untrusted.

## Agent Roles

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

## Configuration Examples

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
also need the Go toolchain. Then set `image:` in a thin `base`-composed
harness (see [Configuring existing agents](bring-your-own-agent.md#configuring-existing-agents)).
Pin the parent tag to a digest before CI use.

### Adding Executables

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

#### Advanced: modifying the PATH for external toolchains

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

### Adding a Skill

> **Deprecated:** The `customized/skills/` path is deprecated.
> Use config-driven agent registration instead — see
> [Bring Your Own Agent](bring-your-own-agent.md).

Create `.fullsend/customized/skills/my-skill/SKILL.md` in your config repo:

```markdown
# My Custom Skill

Custom domain knowledge for this organization.

## Examples

...
```

The skill will be automatically available to all agents that include `skills/my-skill/` in their harness configuration.

### Overriding an Agent Definition

> **Deprecated:** The `customized/agents/` override is deprecated.
> Use `base:` composition instead — see
> [Configuring existing agents](bring-your-own-agent.md#configuring-existing-agents).

Create `.fullsend/customized/agents/code.md` to override the default code agent
with org-specific instructions:

```markdown
# Code Agent (Configured)

[Org-specific instructions...]
```

### Configuring the Harness

Create `.fullsend/customized/harness/code.yaml` to override the code agent's execution environment.

**Important:** This is a complete file replacement. Start by copying the upstream harness, then modify it:

```yaml
# Copy of upstream code.yaml with configuration overrides
agent: agents/code.md
model: claude-opus-4-6           # Changed from: opus
image: ghcr.io/fullsend-ai/fullsend-code:latest
policy: policies/base.yaml
providers:
  - vertex-ai
  - github
  - package-registries
timeout_minutes: 45              # Changed from: 35

skills:
  - skills/code-implementation
  - skills/my-custom-linting     # Added: org-specific skill configuration

plugins:
  - plugins/gopls-lsp

host_files:
  - src: env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /sandbox/workspace/.gcp-oidc-token
    optional: true

pre_script: scripts/pre-code.sh
post_script: scripts/post-code.sh

validation_loop:
  script: scripts/org-validate.sh      # Changed script
  schema: schemas/org-result.schema.json
  max_iterations: 5                   # Changed from: 2

env:
  runner:
    PUSH_TOKEN: "${PUSH_TOKEN}"
    REPO_FULL_NAME: "${REPO_FULL_NAME}"
    REPO_DIR: "${GITHUB_WORKSPACE}/target-repo"
```

Then create your skill at `.fullsend/customized/skills/my-custom-linting/SKILL.md`.

### Per-Repo Overrides

Target repos can override org-level configuration by placing files in `.fullsend/customized/` within the repo:

```
my-repo/
├── .fullsend/
│   └── customized/
│       ├── agents/code.md         # Repo-specific agent configuration
│       ├── skills/repo-skill/     # Repo-specific skill (contains SKILL.md)
│       └── harness/code.yaml      # Repo-specific harness configuration
```

## Disabling Agents

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

## See Also

- [Bring Your Own Agent](bring-your-own-agent.md) - Building and registering custom agents from scratch
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) - When does configuration cross into derived or custom agent territory?
- [Escalation ladder](../../agents/topics/escalation-ladder.md) - Prove-it path before deriving or replacing a core agent
- [Getting Started](../getting-started/) - Initial setup
- [Bugfix Workflow](bugfix-workflow.md) - How agents work together
- [Standalone Mint](../infrastructure/standalone-mint.md) - Running your own mint with custom agent roles
