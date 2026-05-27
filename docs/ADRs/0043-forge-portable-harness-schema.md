---
title: "43. Forge-portable harness schema"
status: Proposed
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - forge
  - portability
  - configuration
---

# 43. Forge-portable harness schema

Date: 2026-05-27

## Status

Proposed

## Context

ADR-0024 established the harness YAML as the self-contained execution unit
for a single agent. It declares the agent definition, model, image, policy,
skills, scripts, host files, validation loop, runner environment, and timeout.
The runner reads one harness file and provisions one sandbox for one agent.

However, two pieces of agent identity — `role` and `slug` — live outside the
harness. They reside in `config.yaml`'s `agents:` block
([ADR 0011](0011-admin-install-org-config-yaml-v1.md)):

```yaml
# config.yaml (current)
agents:
  - role: triage
    name: fullsend-ai-triage
    slug: fullsend-ai-triage
  - role: coder
    name: fullsend-ai-coder
    slug: fullsend-ai-coder
```

This means adding a new agent today requires editing three places:

1. Create the harness YAML (`harness/<agent>.yaml`)
2. Create the agent definition (`agents/<agent>.md`)
3. Add an entry to `config.yaml`'s `agents:` block with the role and slug

Step 3 breaks the self-containment principle from ADR-0024. The harness
declares *everything* the runner needs to execute an agent — except what the
agent *is* (its role in the pipeline) and *who it acts as* (its slug for
forge authentication). These are core identity properties that belong with
the execution definition, not in a separate operational config file.

A second problem emerged with ADR-0028 (GitLab Support Architecture): several
harness fields are inherently forge-specific. Pre/post scripts often contain
forge-specific CLI calls (`gh` vs `glab`). Skills may reference forge-specific
APIs. Runner environment variables carry forge-specific tokens and event URLs.
Today these fields sit at the harness top level, making the entire harness
implicitly GitHub-specific even though the agent runtime itself is
forge-agnostic.

The combination of these two problems — identity outside the harness and
forge-specific config mixed with forge-neutral config — makes harnesses
non-portable. A harness designed for GitHub cannot be used on GitLab without
rewriting the entire file, even though most of its content (agent, model,
image, policy, host files, timeout) is platform-neutral.

### Related work

- [ADR 0024](0024-harness-definitions.md): Established harness YAML as
  self-contained execution unit. This ADR extends its schema.
- [ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md):
  Allowed agents to be added by existence (stage markers in workflows),
  reducing coupling between shim and agent inventory. This ADR applies the
  same principle to harness identity.
- [ADR 0028](0028-gitlab-support.md): GitLab support architecture.
  Identified the need for forge-specific dispatch, authentication, and event
  handling alongside forge-neutral agent execution.
- [ADR 0038](0038-universal-harness-access.md): URL-based resource fetching
  for portable harness resources. Complements this ADR — ADR-0038 makes
  *what the harness references* portable; this ADR makes *the harness itself*
  portable.
- [PR #1259](https://github.com/fullsend-ai/fullsend/pull/1259): Extracting
  GitHub-specific CLI operations behind a separate sub-command tree,
  demonstrating the forge-specific / forge-neutral split in the CLI layer.
- [PR #390](https://github.com/fullsend-ai/fullsend/pull/390): Stage-based
  dispatch decoupling implementation.
- [Issue #101](https://github.com/fullsend-ai/fullsend/issues/101):
  Forge-agnostic agent interface.
- [Issue #322](https://github.com/fullsend-ai/fullsend/issues/322):
  Identified platform-specific parts (dispatch, pre/post scripts, credential
  shape).

## Decision

Extend the harness YAML schema with `role`, `slug`, and a `forge:` section
that separates platform-specific configuration from platform-neutral core
config. Forge-specific blocks inherit from the harness top level and override
only the fields they need, so harness authors write shared defaults once and
supply only per-forge deltas.

This combines two ideas:

1. **Forge section** — a `forge:` map groups platform-specific configuration
   under forge-keyed sub-blocks (`forge.github`, `forge.gitlab`). A single
   harness file serves all supported forges.

2. **Inheritance with overrides** — all fields that can appear under
   `forge.<platform>` can also appear at the harness top level as defaults.
   A forge block inherits every top-level value and overrides only what
   differs. This avoids duplicating shared config across forge blocks while
   keeping forge-specific config explicit.

### Schema changes

#### New top-level fields

```yaml
# Agent identity — previously in config.yaml agents: block
role: triage               # The agent's role in the pipeline (triage, coder, review, fix, etc.)
slug: fullsend-ai-triage   # The forge app/token slug used for authentication
```

`role` identifies the agent's function in the pipeline. `slug` identifies
the forge credential (GitHub App slug, GitLab Project Access Token name)
used when this agent authenticates against the forge API.

#### Forge section with inheritance

```yaml
forge:
  github:
    pre_script: scripts/pre-triage.sh
    post_script: scripts/post-triage.sh
    skills:
      - skills/github-issue-triage
    runner_env:
      GH_TOKEN: ${GH_TOKEN}
      GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
  gitlab:
    pre_script: scripts/pre-triage-gl.sh
    post_script: scripts/post-triage-gl.sh
    skills:
      - skills/gitlab-issue-triage
    runner_env:
      GITLAB_TOKEN: ${GITLAB_TOKEN}
      GITLAB_ISSUE_URL: ${GITLAB_ISSUE_URL}
```

Each key under `forge:` is a platform identifier. The runner selects the
block matching the detected platform, then merges it with top-level defaults
using the inheritance rules described below.

#### Inheritance rules

Fields that appear in both the top level and a `forge.<platform>` block are
resolved as follows:

| Field type       | Merge behavior                                       |
|------------------|------------------------------------------------------|
| Scalar fields    | Forge value overrides top-level value                |
| `skills`         | Top-level list + forge-specific list (concatenated)  |
| `runner_env`     | Top-level map merged with forge map; forge keys win  |
| `validation_loop`| Forge value replaces top-level value entirely        |

This means a harness can define shared defaults at the top level and
forge-specific deltas in each forge block:

```yaml
# Shared defaults (inherited by all forges)
pre_script: scripts/pre-common.sh
skills:
  - skills/issue-labels
  - skills/output-schema-validation
runner_env:
  FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/schemas/triage-result.schema.json
validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2

forge:
  github:
    pre_script: scripts/pre-triage-gh.sh   # overrides top-level pre_script
    skills:
      - skills/github-issue-triage         # appended to top-level skills
    runner_env:
      GH_TOKEN: ${GH_TOKEN}               # added; FULLSEND_OUTPUT_SCHEMA inherited
      GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
    # validation_loop: inherited from top level (same script works on both)
  gitlab:
    pre_script: scripts/pre-triage-gl.sh   # overrides top-level pre_script
    skills:
      - skills/gitlab-issue-triage         # appended to top-level skills
    runner_env:
      GITLAB_ISSUE_URL: ${GITLAB_ISSUE_URL}
    # validation_loop: inherited from top level
```

Effective config on GitHub:
- `pre_script`: `scripts/pre-triage-gh.sh` (overridden)
- `post_script`: none (not set at either level)
- `skills`: `[issue-labels, output-schema-validation, github-issue-triage]`
- `runner_env`: `{FULLSEND_OUTPUT_SCHEMA: ..., GH_TOKEN: ..., GITHUB_ISSUE_URL: ...}`
- `validation_loop`: inherited from top level

When no `forge:` section is present, the harness works exactly as it does
today — all top-level fields are used directly. This provides full backward
compatibility.

#### Fields that can appear at both levels

| Field              | Rationale                                          |
|--------------------|----------------------------------------------------|
| `pre_script`       | Scripts often call forge-specific CLIs (gh, glab)  |
| `post_script`      | Push, PR/MR creation is forge-specific             |
| `skills`           | Some skills wrap forge-specific APIs               |
| `runner_env`       | Token names and event URLs differ per forge        |
| `validation_loop`  | Validation scripts may call forge-specific tools   |

#### Fields that stay at top level only (platform-neutral)

| Field              | Rationale                                          |
|--------------------|----------------------------------------------------|
| `agent`            | Agent definitions are forge-agnostic               |
| `model`            | Model selection is independent of forge             |
| `image`            | Container images are platform-neutral              |
| `policy`           | Sandbox policies describe capabilities, not forges |
| `host_files`       | File delivery is a runner concern, not forge        |
| `providers`        | OpenShell providers are forge-agnostic             |
| `api_servers`      | REST proxies abstract forge details                |
| `timeout_minutes`  | Timeouts are operational, not forge-specific        |
| `security`         | Security scanning is forge-agnostic                |
| `description`      | Documentation, no runtime effect                   |
| `role`             | Agent identity is forge-agnostic                   |
| `slug`             | Can be overridden via config.yaml, not per-forge   |

### Full example

```yaml
# harness/triage.yaml
agent: agents/triage.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/triage.yaml

role: triage
slug: fullsend-ai-triage

# Shared across all forges
skills:
  - skills/issue-labels
validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2
runner_env:
  FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/schemas/triage-result.schema.json

forge:
  github:
    pre_script: scripts/pre-triage.sh
    post_script: scripts/post-triage.sh
    skills:
      - skills/github-issue-triage
    runner_env:
      GH_TOKEN: ${GH_TOKEN}
      GITHUB_ISSUE_URL: ${GITHUB_ISSUE_URL}
  gitlab:
    pre_script: scripts/pre-triage-gl.sh
    post_script: scripts/post-triage-gl.sh
    skills:
      - skills/gitlab-issue-triage
    runner_env:
      GITLAB_ISSUE_URL: ${GITLAB_ISSUE_URL}

host_files:
  - src: env/gcp-vertex.env
    dest: /tmp/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/workspace/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /tmp/workspace/.gcp-oidc-token
    optional: true
  - src: env/triage.env
    dest: /tmp/workspace/.env.d/triage.env
    expand: true

timeout_minutes: 10
```

### What stays in config.yaml

`config.yaml` retains operational state that does not belong in individual
harness files:

| Field                   | Purpose                                          |
|-------------------------|--------------------------------------------------|
| `version`               | Schema version                                   |
| `kill_switch`           | Org-wide emergency stop                          |
| `dispatch`              | Platform and dispatch mode (oidc-mint, etc.)     |
| `inference`             | Inference provider (vertex, etc.)                |
| `defaults.roles`        | Which roles are active by default for new repos  |
| `defaults.max_implementation_retries` | Org-wide retry policy        |
| `defaults.auto_merge`   | Org-wide auto-merge policy                       |
| `repos`                 | Per-repo enabled/disabled and role overrides      |
| `allowed_remote_resources` | URL allowlist for remote harness resources    |

The `agents:` block in config.yaml becomes optional. When present, it
provides slug overrides — for example, when an org uses non-default GitHub
App naming. When absent, the runner reads `role` and `slug` from the harness
file directly. If both the harness and config.yaml specify a slug for the
same role, config.yaml wins (operational override takes precedence over
harness defaults).

### Forge block struct (Go)

```go
// ForgeConfig holds platform-specific harness configuration.
// Fields set here override or extend the corresponding top-level
// harness fields via the inheritance rules in ADR-0043.
type ForgeConfig struct {
    PreScript      string            `yaml:"pre_script,omitempty"`
    PostScript     string            `yaml:"post_script,omitempty"`
    Skills         []string          `yaml:"skills,omitempty"`
    ValidationLoop *ValidationLoop   `yaml:"validation_loop,omitempty"`
    RunnerEnv      map[string]string `yaml:"runner_env,omitempty"`
}

// Updated Harness struct (additions only)
type Harness struct {
    // ... existing platform-neutral fields ...
    Role  string                  `yaml:"role,omitempty"`
    Slug  string                  `yaml:"slug,omitempty"`
    Forge map[string]*ForgeConfig `yaml:"forge,omitempty"`
}

// ResolveForge returns the effective harness config for the given platform
// by merging top-level defaults with the forge-specific overrides.
func (h *Harness) ResolveForge(platform string) *Harness { ... }
```

### Migration path

1. **Phase 1 (backward compatible):** Add `role`, `slug`, and `forge:` to
   the harness schema as optional fields. The runner checks the harness first;
   if `role`/`slug` are missing, it falls back to `config.yaml`'s `agents:`
   block. Top-level `pre_script`, `post_script`, `skills`, `runner_env`, and
   `validation_loop` continue to work as they do today — they serve as
   defaults inherited by all forge blocks. When no `forge:` section is
   present, the harness behaves identically to the current schema.

2. **Phase 2 (adopt):** Migrate existing harnesses to include `role` and
   `slug`. Harnesses that only target GitHub can optionally add
   `forge.github` but are not required to — top-level fields still work
   as implicit defaults for the single-forge case.

3. **Phase 3 (deprecate):** Deprecate the `agents:` block requirement from
   config.yaml. The `agents:` block becomes purely optional for slug
   overrides. Emit warnings when `role` is missing from a harness file.

4. **Phase 4 (remove):** Require `role` in all harness files. Remove the
   `agents:` block fallback from the runner. Config.yaml `agents:` remains
   available solely for slug overrides.

### Adding a new agent (after migration)

Before this ADR, adding a new agent required:
1. Create `harness/<agent>.yaml`
2. Create `agents/<agent>.md`
3. Create the CI workflow (`.github/workflows/<agent>.yml`)
4. Add an entry to `config.yaml`'s `agents:` block

After this ADR, step 4 is eliminated:
1. Create `harness/<agent>.yaml` (includes role and slug)
2. Create `agents/<agent>.md`
3. Create the CI workflow

Combined with ADR-0026 (stage markers), the CI workflow is the only
forge-specific artifact. The harness and agent definition are portable.

## Consequences

- **Harnesses become the source of truth for agent identity.** `role` and
  `slug` live alongside the execution config they govern. The runner no
  longer needs to cross-reference `config.yaml` to know what an agent is
  or how it authenticates.

- **Single file, multiple forges.** One harness file can target GitHub and
  GitLab (and future forges) simultaneously. The runner selects the
  appropriate `forge.<platform>` block at runtime and merges it with
  top-level defaults. Platform-neutral fields and shared forge config are
  written once.

- **Inheritance reduces duplication.** Shared scripts, skills, runner_env,
  and validation loops are defined once at the top level. Forge blocks only
  specify what differs. A harness targeting a single forge needs no `forge:`
  section at all — top-level fields serve as the complete config.

- **Reduced friction for adding agents.** Eliminating the config.yaml
  `agents:` entry removes a coordination step. Agent authors own their
  entire definition in the harness + agent .md + workflow.

- **Clear forge boundary.** Harness authors can see at a glance which parts
  of their configuration are forge-dependent. This makes porting to a new
  forge a scoped task: add a `forge.<new-platform>` block with only the
  deltas from the shared defaults.

- **config.yaml becomes purely operational.** It retains org-wide settings
  (kill switch, defaults, per-repo config, URL allowlists) and optional slug
  overrides. It no longer defines the agent inventory — that is discovered
  from harness files.

- **Merge semantics add complexity.** The inheritance rules (scalars
  override, skills concatenate, runner_env merges, validation_loop replaces)
  must be well-documented and tested. Edge cases — such as a forge block
  wanting to *remove* an inherited skill or runner_env key — are not
  supported by this design. If needed, a future extension could add explicit
  `exclude_skills` or similar fields.

- **Backward compatibility during migration.** Phase 1 maintains full
  backward compatibility. Existing harnesses work unchanged. This avoids a
  flag day migration across all deployed configurations.

- **The Harness struct grows.** The `forge` field adds a map of
  `ForgeConfig` structs. Validation must check that forge keys are
  recognized platform identifiers and that forge-specific fields pass the
  same validation as their top-level counterparts (script paths exist,
  runner_env vars are set, etc.).

- **Agent discovery changes.** Today the runner discovers available agents
  from `config.yaml`'s `agents:` block. After this change, agent discovery
  can scan `harness/*.yaml` files and read `role` from each. This aligns
  with ADR-0026's model where agents are discovered by existence, not by
  central registration.

## Open questions

- **Forge detection at runtime.** How does the runner determine which
  `forge.<platform>` block to select? Candidates: (a) the `dispatch.platform`
  field in `config.yaml`, (b) environment variable inspection (e.g.,
  `GITHUB_ACTIONS=true`, `GITLAB_CI=true`), (c) explicit CLI flag
  (`fullsend run --forge github triage`). Option (a) is the current path of
  least resistance since `dispatch.platform` already exists. Option (b) is
  more portable but fragile. Option (c) is most explicit but adds CLI
  surface. These are not mutually exclusive — a precedence chain
  (flag > env > config) could work.

- **Excluding inherited values.** The current design does not support
  removing an inherited skill or runner_env key in a forge block. If a
  top-level skill is inappropriate for one forge, the harness author must
  move it out of the top level and into each forge block individually. Is
  this acceptable, or should we support explicit exclusion (e.g.,
  `exclude_skills: [skills/issue-labels]`)?

- **Slug derivation convention.** If `slug` is omitted from the harness,
  should the runner derive it from the role using a convention
  (e.g., `<org>-<role>`)? This would eliminate the `slug` field for the
  common case but introduces an implicit naming contract. The alternative
  is requiring `slug` whenever `role` is set.

- **Pre/post script overlap.** Some pre/post script logic is shared across
  forges (e.g., cloning, environment setup) with only small forge-specific
  sections (e.g., `gh pr create` vs `glab mr create`). Should the harness
  support a shared pre/post script that calls a forge-specific helper, or
  should each forge provide its own complete script? The current design
  requires complete per-forge scripts (scalar override), which may lead to
  duplication. Script-level factoring (shared functions sourced by
  forge-specific scripts) is a convention, not a schema concern.

- **config.yaml agents: block removal timeline.** When can the `agents:`
  block be fully removed from config.yaml? This depends on how many
  consumers read it directly. The admin install flow
  (`internal/appsetup/`) currently writes it during GitHub App creation.
  Migration requires updating appsetup to write `role`/`slug` into harness
  files instead.

## References

- ADR-0005: Forge abstraction layer
- ADR-0011: Canonical schema for admin-managed org config.yaml (v1)
- ADR-0024: Harness definitions and shared directory layout
- ADR-0026: Stage-based dispatch for agent workflow decoupling
- ADR-0028: GitLab Support Architecture
- ADR-0038: Universal harness access via URLs and paths
- [PR #1259](https://github.com/fullsend-ai/fullsend/pull/1259): GitHub-specific CLI sub-command extraction
- [Issue #101](https://github.com/fullsend-ai/fullsend/issues/101): Forge-agnostic agent interface
- [Issue #322](https://github.com/fullsend-ai/fullsend/issues/322): Platform-specific component identification
