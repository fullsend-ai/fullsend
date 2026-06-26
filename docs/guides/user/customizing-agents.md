# Customizing Agents

This guide explains how to customize fullsend agents for your organization and repositories through harness configurations and layered content resolution.

## Harness Configuration

Each agent run is configured by a harness YAML file that defines the complete execution environment. These files live in the `.fullsend` config repo (per-org mode) or `.fullsend/customized/harness/` (per-repo mode).

### Harness YAML Structure

A minimal harness configuration (based on actual fullsend agent harnesses):

```yaml
agent: agents/code.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-code:latest
policy: policies/code.yaml
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
  max_iterations: 2

runner_env:
  PUSH_TOKEN: "${PUSH_TOKEN}"  # auto-minted in CI when --mint-url is provided
  REPO_FULL_NAME: "${REPO_FULL_NAME}"
  REPO_DIR: "${GITHUB_WORKSPACE}/target-repo"
```

**Optional fields** (all have secure defaults and can be omitted):

```yaml
providers:                       # Inference providers (loaded from providers/ dir)
  - vertex                       # References providers/vertex.yaml

validation_loop:
  feedback_mode: stderr          # "stderr", "stdout", or "exit_code" (optional)

allowed_remote_resources:        # URL prefixes allowed for remote skills/agents/policies
  - https://github.com/org/       # Resources must match a prefix to be fetched
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

## Layered Configuration Resolution

Fullsend uses a three-tier inheritance model for all configuration: agent definitions, skills, policies, harness definitions, and guardrails. Each tier can extend or override the one below it.

```
┌──────────────────────────────────────────────────────────────┐
│              Configuration Layering (ADR 0035)                │
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
│    agents/  skills/  schemas/  harness/  plugins/             │
│    policies/  scripts/  env/                                 │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

### Customization Directory Structure

Orgs customize layered directories by placing overrides in `customized/` subdirectories:

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

**File-level replacement, not field-level merging.** When you place a file in `customized/harness/code.yaml`, it completely replaces the upstream `harness/code.yaml`. There is no YAML field merging.

**Example: Adding a skill to the code agent**

To add a custom skill to the code agent's harness:

1. **Copy the full upstream harness** from `fullsend-ai/fullsend` to your customization directory:
   ```bash
   # Get upstream harness
   curl -o .fullsend/customized/harness/code.yaml \
     https://raw.githubusercontent.com/fullsend-ai/fullsend/main/internal/scaffold/fullsend-repo/harness/code.yaml
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

### Customizing Pre-commit Tool Dependencies

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

See [ADR 0056](../../ADRs/0056-per-repo-precommit-tools-registry.md) for the full security rationale.

## Agent Roles

Each agent role has its own identity, permissions, and purpose:

```
┌─────────────────────────────────────────────────────────────┐
│                   Agent Role Architecture                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Role          GitHub App                  Purpose          │
│  ─────         ──────────                  ───────          │
│  fullsend      {org}-fullsend[bot]         Dispatch/control │
│  triage        {org}-triage[bot]           Issue triage     │
│  coder         {org}-coder[bot]            Code generation  │
│  review        {org}-review[bot]           PR review        │
│  fix           (reuses coder app)          Fix failures     │
│  retro         {org}-retro[bot]            Retrospectives   │
│  prioritize    {org}-prioritize[bot]       Backlog priority │
│                                                             │
│  App naming: {org}-{role}                                   │
│  Bot naming: {org}-{role}[bot]                              │
│  PEM storage: GCP Secret Manager                            │
│  Secret name: fullsend-{role}-app-pem                       │
│                                                             │
│  Note: "fix" role reuses the "coder" app and PEM — no       │
│  separate GitHub App or secret is created for it.          │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Customization Examples

### Adding a Custom Skill

Create `.fullsend/customized/skills/my-skill/SKILL.md` in your config repo:

```markdown
# My Custom Skill

Custom domain knowledge for this organization.

## Examples

...
```

The skill will be automatically available to all agents that include `skills/my-skill/` in their harness configuration.

### Overriding an Agent Definition

Create `.fullsend/customized/agents/code.md` to override the default code agent with org-specific instructions:

```markdown
# Code Agent (Customized)

[Custom instructions for your org...]
```

### Customizing Harness Configuration

Create `.fullsend/customized/harness/code.yaml` to override the code agent's execution environment.

**Important:** This is a complete file replacement. Start by copying the upstream harness, then modify it:

```yaml
# Copy of upstream code.yaml with customizations
agent: agents/code.md
model: claude-opus-4-6           # Changed from: opus
image: ghcr.io/fullsend-ai/fullsend-code:latest
policy: policies/code.yaml
timeout_minutes: 45              # Changed from: 35

skills:
  - skills/code-implementation
  - skills/my-custom-linting     # Added: org-specific skill

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
  script: scripts/custom-validate.sh  # Changed script
  max_iterations: 5                   # Changed from: 2

runner_env:
  PUSH_TOKEN: "${PUSH_TOKEN}"
  REPO_FULL_NAME: "${REPO_FULL_NAME}"
  REPO_DIR: "${GITHUB_WORKSPACE}/target-repo"
```

Then create your custom skill at `.fullsend/customized/skills/my-custom-linting/SKILL.md`.

### Per-Repo Overrides

Target repos can override org-level customizations by placing files in `.fullsend/customized/` within the repo:

```
my-repo/
├── .fullsend/
│   └── customized/
│       ├── agents/code.md         # Repo-specific agent instructions
│       ├── skills/repo-skill/     # Repo-specific skill (contains SKILL.md)
│       └── harness/code.yaml      # Repo-specific harness config
```

## See Also

- [Installation Guide](../../reference/installation.md) - Initial setup
- [Bugfix Workflow](bugfix-workflow.md) - How agents work together
- [ADR 0035: Layered Content Resolution](../../ADRs/0035-layered-content-resolution.md)
