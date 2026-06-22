---
title: "45. Forge-portable harness schema"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - forge
  - portability
  - configuration
---

# 45. Forge-portable harness schema

Date: 2026-05-27

## Status

Accepted

## Context

ADR 0024 established the harness YAML as the self-contained execution unit
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

Step 3 breaks the self-containment principle from ADR 0024. The harness
declares *everything* the runner needs to execute an agent — except what the
agent *is* (its role in the pipeline) and *who it acts as* (its slug for
forge authentication). These are core identity properties that belong with
the execution definition, not in a separate operational config file.

A second problem emerged with ADR 0028 (GitLab Support Architecture): several
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
- [ADR 0028](0028-gitlab-support.md): GitLab support architecture
  (status: Deprecated). While the specific implementation approach was
  deprecated, ADR 0028's analysis of forge-specific vs. forge-neutral
  component splits remains the foundation for this ADR's design.
- [ADR 0038](0038-universal-harness-access.md): URL-based resource fetching
  for portable harness resources. Complements this ADR — ADR 0038 makes
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

Extend the harness YAML schema with `role`, `slug`, `base`, and a `forge:`
section that separates platform-specific configuration from platform-neutral
core config. Forge-specific blocks inherit from the harness top level and
override only the fields they need, so harness authors write shared defaults
once and supply only per-forge deltas.

This combines three ideas:

1. **Forge section** — a `forge:` map groups platform-specific configuration
   under forge-keyed sub-blocks (`forge.github`, `forge.gitlab`). A single
   harness file serves all supported forges.

2. **Inheritance with overrides** — all fields that can appear under
   `forge.<platform>` can also appear at the harness top level as defaults.
   A forge block inherits every top-level value and overrides only what
   differs. This avoids duplicating shared config across forge blocks while
   keeping forge-specific config explicit.

3. **Harness composition via `base`** — a harness can reference another
   harness file (local path or URL) as its foundation. The child harness
   inherits all fields from the base and overrides only what differs, using
   the same merge rules as forge blocks. This replaces config.yaml as the
   override mechanism and enables cross-org harness sharing.

### Schema changes

#### New top-level fields

```yaml
# Agent identity — previously in config.yaml agents: block
role: triage               # The agent's role in the pipeline (triage, coder, review, fix, etc.)
slug: fullsend-ai-triage   # The forge app/token slug used for authentication

# Harness composition — inherit from another harness
base: harness/common-triage.yaml  # optional: path or URL to a base harness
```

`role` identifies the agent's function in the pipeline. `slug` identifies
the forge credential (GitHub App slug, GitLab Project Access Token name)
used when this agent authenticates against the forge API.

These fields are distinct from the existing `agent` field (path to the
agent definition `.md` file). `agent` describes *how* the agent behaves;
`role` describes *what function* the agent serves in the pipeline; `slug`
describes *who* the agent authenticates as. During Phase 1-2, `role` and
`slug` are optional — `Validate()` does not require them. In Phase 3,
`Validate()` continues to allow missing `role`, but `Lint()` emits
warnings when `role` is missing. In Phase 4, `Validate()` requires
`role`.

`base` references another harness file whose fields serve as defaults for
this harness. Any field set in the child overrides the corresponding base
field using the same merge rules defined in the inheritance table below.
`base` can be a local path (resolved via `ResolveRelativeTo`) or a URL
(reusing ADR 0038's fetch infrastructure with mandatory `#sha256=...`
integrity hash). See "Harness composition via `base`" below for details.

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

| Field type       | Merge behavior                                       | Nil vs empty                                          |
|------------------|------------------------------------------------------|-------------------------------------------------------|
| Scalar fields    | Forge value overrides top-level value                | Absent = inherit from top level                       |
| `skills`         | Top-level list + forge-specific list (concatenated)  | Absent (nil) = inherit; `skills: []` = no forge-specific additions (top-level skills still apply) |
| `runner_env`     | Top-level map merged with forge map; forge keys win  | Absent (nil) = inherit; `runner_env: {}` = no forge-specific keys (top-level env still inherited) |
| `validation_loop`| Forge value replaces top-level value entirely        | Absent (nil) = inherit from top level; explicit empty struct = intended to mean "no validation" but requires implementation changes (see note¹) |

¹ An explicit empty `validation_loop: {}` currently conflicts with
`Validate()`, which requires `Script` when `ValidationLoop` is non-nil.
See the Open Questions nil-vs-empty section for resolution options.

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
| `plugins`          | MCP plugins are forge-agnostic                     |
| `agent_input`      | Agent prompt input is forge-agnostic               |
| `timeout_minutes`  | Timeouts are operational, not forge-specific        |
| `sandbox_timeout_seconds` | Sandbox-level timeout, not forge-specific   |
| `security`         | Security scanning is forge-agnostic                |
| `allowed_remote_resources` | URL allowlist for resource fetching (ADR 0038) |
| `description`      | Documentation, no runtime effect                   |
| `role`             | Agent identity is forge-agnostic                   |
| `slug`             | Kept top-level; per-forge slug differences handled via `base` composition or a future `forge.<platform>.slug` extension — see trade-off note below |
| `base`             | Composition is a structural concern, not forge-specific |

**Slug trade-off:** In multi-forge deployments, different forges may require
different slug values (e.g., a GitHub App slug vs a GitLab Project Access
Token name). Rather than allowing `slug` inside `forge.<platform>` blocks,
this design keeps `slug` top-level. If per-forge slugs become common
enough to justify first-class support, `slug` can be added to the
`ForgeConfig` overridable fields in a future revision.

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
      GITLAB_TOKEN: ${GITLAB_TOKEN}
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

### Harness composition via `base`

A harness can reference another harness as its foundation using the `base`
field. The base harness is loaded first, then the child's fields are merged
on top using the same inheritance rules defined above. This enables
cross-org harness sharing and replaces config.yaml as the override
mechanism for harness fields.

#### Resolution order

```
base harness (recursive) → child overrides → ResolveForge(platform)
```

The base is resolved first (recursively, if the base itself has a `base`).
The child's fields are merged on top of the fully-resolved base. Then
`ResolveForge(platform)` runs once on the merged result. The `base` field
itself is consumed during loading and is not present on the merged harness.

#### Merge rules

The same inheritance table applies to base→child merging:

- **Scalar fields** (agent, model, image, pre_script, etc.): child overrides base
- **`skills`**: base list + child list (concatenated)
- **`runner_env`**: base map merged with child map; child keys win
- **`validation_loop`**: child replaces base entirely (if non-nil)
- **`host_files`**: concatenated (base + child); if both declare the same
  `dest` path, the child entry wins (last-writer-wins deduplication)
- **`plugins`, `providers`, `api_servers`**: concatenated (base + child)
- **`security`**: child replaces base entirely (if non-nil)
- **`forge` blocks**: child's `forge:` map merges key-by-key with base's
  `forge:` map. For each platform key present in both, the per-platform
  `ForgeConfig` fields merge using the rules above.

#### URL support

`base` can be a URL, reusing ADR 0038's infrastructure:

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/triage.yaml#sha256=abc123...
```

URL-referenced bases follow the same rules as other URL resources:
HTTPS only, mandatory `#sha256=...` integrity hash, must be covered by
the org's `allowed_remote_resources` allowlist, fetched via the
SSRF-hardened fetch layer, and cached in `.fullsend-cache/`.

Relative paths in the merged result (e.g., `pre_script: scripts/pre.sh`)
resolve against the local `.fullsend/` directory when the base is a
local file. When the base is a URL, script fields (`pre_script`,
`post_script`, `validation_loop.script`) declared in the base harness
are fetched from the base URL's directory, cached content-addressed,
and rewritten to local cache paths before validation (see ADR 0038's
`base:` composition exception). `agent_input` is excluded from URL-base
resolution because it is a directory, not a single file. Scripts in the
child harness always resolve against the local `.fullsend/` directory.

#### Depth limit and circular detection

Base chains are limited to 5 levels. A visited set of canonical
paths/URLs prevents circular references (`A → B → A`). Local paths
must be canonicalized (e.g., `filepath.Clean` + `filepath.Abs`) before
insertion into the visited set so that equivalent paths like
`harness/../harness/triage.yaml` and `harness/triage.yaml` are detected
as the same file.

#### Example: composed harness

A fresh `fullsend install` generates thin harness wrappers:

```yaml
# .fullsend/harness/triage.yaml
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/triage.yaml#sha256=...
```

An org that needs to customize:

```yaml
# .fullsend/harness/triage.yaml
base: https://raw.githubusercontent.com/fullsend-ai/fullsend/<sha>/internal/scaffold/fullsend-repo/harness/triage.yaml#sha256=...
slug: my-org-triage          # override slug
model: sonnet                # override model
runner_env:
  CUSTOM_VAR: value          # merged with base runner_env
```

Removing an agent = delete the harness file. Adding one back = create a
one-line file with `base:`. This is the same operation for default and
third-party agents — no invisible scaffold layering, no hardcoded role
lists.

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

The `agents:` block in config.yaml is removed. All agent identity and
configuration — including `role`, `slug`, and any field-level
customization — lives in harness files. Orgs that need to override a
default harness's slug or other fields do so via `base` composition: a
thin child harness referencing the upstream harness by URL and overriding
only the fields that differ. This keeps agent configuration fully
self-contained (ADR 0024) and avoids splitting an agent's definition
across two files.

config.yaml does not gain deep-merge capabilities or per-agent override
entries. Harness-to-harness composition via the `base` field provides
field-level customization without coupling harness configuration to the
operational config file.

### Forge block struct (Go)

```go
// ForgeConfig holds platform-specific harness configuration.
// This is purely declarative YAML config — it selects which
// scripts, skills, and env vars to use per platform. It is
// distinct from the forge.Client interface (internal/forge/),
// which is the runtime abstraction for forge API operations.
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
    Base  string                  `yaml:"base,omitempty"`
    Role  string                  `yaml:"role,omitempty"`
    Slug  string                  `yaml:"slug,omitempty"`
    Forge map[string]*ForgeConfig `yaml:"forge,omitempty"`
}

// ResolveForge merges forge-specific overrides into the harness in place.
// Must be called BEFORE ResolveRelativeTo() and ValidateFilesExist(),
// since those methods resolve paths for fields (PreScript, PostScript,
// Skills, RunnerEnv, ValidationLoop) that forge overrides may replace.
//
// Integration point: insert between Unmarshal and Validate in Load().
// Must NOT be called externally after Load(), since Load() calls
// Validate() which would reject sentinel values (e.g., empty
// validation_loop structs) before ResolveForge processes them.
func (h *Harness) ResolveForge(platform string) error { ... }

```

### Migration path

1. **Phase 1 (backward compatible):** Add `role`, `slug`, `base`, and
   `forge:` to the harness schema as optional fields. The runner checks the
   harness first; if `role`/`slug` are missing, it falls back to
   `config.yaml`'s `agents:` block (backward compatibility only — this
   fallback is removed in Phase 4). When `base` is present, the runner
   loads and merges the base before proceeding. Top-level `pre_script`,
   `post_script`, `skills`, `runner_env`, and `validation_loop` continue
   to work as they do today — they serve as defaults inherited by all forge
   blocks. When no `forge:` section is present, the harness behaves
   identically to the current schema.

2. **Phase 2 (adopt):** Migrate existing harnesses to include `role` and
   `slug`. `fullsend install` generates thin harness wrappers with `base:`
   pointing to upstream scaffold harnesses via URL. Harnesses that only
   target GitHub can optionally add `forge.github` but are not required
   to — top-level fields still work as implicit defaults for the
   single-forge case.

3. **Phase 3 (deprecate):** Deprecate the `agents:` block in config.yaml.
   Emit warnings when `role` is missing from a harness file. All agent
   identity and configuration should be migrated to harness files; the
   `agents:` block no longer serves any override purpose.
   Note: `role`/`slug` becoming required is independent of the `forge:`
   section — a harness that only targets one platform still needs `role`
   and `slug` but does not need `forge:`.
   Implementation note: `Validate()` returns hard errors only. Phase 3
   adds a separate `Lint()` method that returns non-fatal `[]Diagnostic`
   warnings without breaking existing callers that treat any `Validate()`
   error as a hard stop.

4. **Phase 4 (remove):** Require `role` in all harness files. Remove the
   `agents:` block from config.yaml entirely. Agent identity and
   configuration live exclusively in harness files; any customization
   uses `base` composition.

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

Combined with ADR 0026 (stage markers), the CI workflow is the only
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
  (kill switch, dispatch, defaults, per-repo config, URL allowlists). It no
  longer defines the agent inventory or provides per-agent overrides — agent
  discovery and configuration live entirely in harness files. Harness
  composition via `base` serves the customization role instead.

- **Cross-org harness sharing via `base`.** Downstream orgs can reference
  upstream harness files by URL, overriding only the fields they need.
  Default agents and custom agents use the same delivery mechanism —
  removing an agent is deleting a file, adding one is creating a thin
  wrapper with `base:`.

- **Default URL allowlist for `base` composition.** `fullsend install`
  sets `allowed_remote_resources` in `config.yaml` to include the
  fullsend scaffold URL prefix
  (`https://raw.githubusercontent.com/fullsend-ai/fullsend/`), ensuring
  generated `base:` URLs pass the allowlist without manual configuration.
  Integrity is enforced by the mandatory `#sha256=...` hash in each URL.

- **Phase 2 dual-write.** During Phase 2, agent identity (`role`, `slug`)
  is written to both `config.yaml`'s `agents:` block and harness wrapper
  files. The `agents:` block remains the source of truth for existing
  consumers (`loadKnownSlugs`, `runUninstall`, `SecretsLayer`). Phase 3
  migrates consumers to harness-file discovery; Phase 4 removes the
  `agents:` block. Reconciliation between the two is not needed because
  both are written atomically during `fullsend install`.

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
  `ForgeConfig` structs. The initial set of recognized forge keys is
  `github` and `gitlab`. `Validate()` rejects unrecognized keys with an
  error listing the valid options, so typos like `forge: gihub:` fail
  loudly rather than falling through to top-level defaults silently.
  Forge-specific fields pass the same validation as their top-level
  counterparts (script paths exist, runner_env vars are set, etc.).

- **Agent discovery changes.** Today the runner discovers available agents
  from `config.yaml`'s `agents:` block. After this change, agent discovery
  can scan `harness/*.yaml` files and read `role` from each. This aligns
  with ADR 0026's model where agents are discovered by existence, not by
  central registration.

## Open questions

- **Forge detection at runtime.** How does the runner determine which
  `forge.<platform>` block to select? Candidates: (a) the `dispatch.platform`
  field in `config.yaml`, (b) environment variable inspection (e.g.,
  `GITHUB_ACTIONS=true`, `GITLAB_CI=true`), (c) explicit CLI flag
  (`fullsend run --forge github triage`). Option (a) is the current path of
  least resistance since `dispatch.platform` exists in config.yaml, though
  its current value (`github-actions`) is a CI platform identifier rather
  than a forge identifier — a mapping layer or separate field would be
  needed. Option (b) is
  more portable but fragile. Option (c) is most explicit but adds CLI
  surface. These are not mutually exclusive — a precedence chain
  (flag > env > config) could work.

- **Nil vs empty in Go YAML unmarshaling.** The `ResolveForge`
  implementation must distinguish between an absent field (nil — inherit
  from top level) and an explicit empty value. Go's YAML unmarshaling
  with `omitempty` produces different zero values for these cases: a nil
  slice vs an empty slice, a nil map vs an empty map, a nil pointer vs a
  zero-value struct. The nil-vs-empty distinction matters differently per
  field type, matching the inheritance rules in the table above:
  - `skills`: nil = inherit top-level list; `skills: []` = no
    forge-specific additions (top-level skills still apply, since skills
    uses concatenation semantics).
  - `runner_env`: nil = inherit top-level map; `runner_env: {}` = no
    forge-specific keys (top-level env still inherited, since runner_env
    uses merge semantics).
  - `validation_loop`: nil = inherit from top level; non-nil (including
    zero struct) = override entirely. Note: an explicit empty struct
    (`validation_loop: {}`) currently conflicts with `Validate()`, which
    requires `Script` when `ValidationLoop` is non-nil. The
    implementation must either set the field to nil to disable validation
    (treating empty struct as a sentinel for "remove"), add a
    `disabled: true` field, or update `Validate()` to accept a
    zero-value struct as "no validation."
  These nil-vs-empty distinctions rely on `gopkg.in/yaml.v3`-specific
  behavior. Unit tests should lock in the expected unmarshaling for each
  field type to prevent regressions if the YAML library changes.

- **Excluding inherited values.** The current design does not support
  removing an inherited skill or runner_env key — whether inherited from a
  forge block's top level or from a `base` harness. If a base declares a
  skill that the child does not want, the child currently has no way to
  remove it. Is this acceptable, or should we support explicit exclusion
  (e.g., `exclude_skills: [skills/issue-labels]`)?

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

- **config.yaml schema versioning.** Removing `agents:` (Phase 4) changes
  the v1 schema contract established by ADR 0011. The current
  `OrgConfig.Agents` field uses `yaml:"agents"` without `omitempty`,
  meaning it is part of the v1 contract. Adding `omitempty` and treating
  absence as "discover from harness files" is likely v1-compatible for
  Phase 3 (deprecation), but full removal in Phase 4 may warrant a v2
  schema. Consumers that assume `Agents` is always populated need
  auditing.
  *Note: Phase 3 PR 6 added `omitempty` to the `Agents` field. The
  Phase 4 plan (`docs/plans/adr-0045-forge-portable-harness-phase4.md`)
  recommends staying on v1 — removal is backward-compatible since
  `yaml.Unmarshal` silently ignores unknown keys.*

- **config.yaml agents: block removal timeline.** The `agents:` block is
  removed entirely in Phase 4. Consumers that read it directly need
  migration. The admin install flow (`internal/appsetup/`) currently
  writes it during GitHub App creation — appsetup must be updated to
  write `role`/`slug` into harness files instead.

- **Relative path resolution in URL-referenced bases.** When a base
  harness is fetched from a URL, how are its own relative paths (e.g.,
  `agent: agents/triage.md`) resolved? Options: (a) reject relative paths
  in URL-referenced bases (require all paths to be URLs or absolute),
  (b) resolve relative to the base URL's path prefix, (c) resolve
  relative to the child harness's directory. Option (c) is the simplest
  for Phase 1 and works because scripts are scaffolded locally — `base`
  handles declarative config, scripts stay local.

- **host_files merge edge cases.** The merge rules specify
  last-writer-wins deduplication by `dest` path when base and child both
  declare the same destination. This handles the common case, but edge
  cases remain: should deduplication be case-sensitive? Should it
  normalize paths (e.g., trailing slashes)? These details are deferred
  to implementation.

- **`base` as a harness fragment.** Should `base` support referencing
  partial YAML (without the required `agent` field)? This would enable
  shared config fragments but requires relaxing `Validate()` for base
  harnesses. The alternative is requiring all bases to be valid complete
  harnesses.

## References

- ADR 0005: Forge abstraction layer
- ADR 0011: Canonical schema for admin-managed org config.yaml (v1)
- ADR 0024: Harness definitions and shared directory layout
- ADR 0026: Stage-based dispatch for agent workflow decoupling
- ADR 0028: GitLab Support Architecture
- ADR 0038: Universal harness access via URLs and paths
- [PR #1259](https://github.com/fullsend-ai/fullsend/pull/1259): GitHub-specific CLI sub-command extraction
- [Issue #101](https://github.com/fullsend-ai/fullsend/issues/101): Forge-agnostic agent interface
- [Issue #322](https://github.com/fullsend-ai/fullsend/issues/322): Platform-specific component identification
- [Issue #1986](https://github.com/fullsend-ai/fullsend/issues/1986): Default agents should use the same delivery mechanism as custom agents
