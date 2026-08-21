# Harness Field Reference

> **This is a living document.** It is the authoritative reference for harness
> field classifications, merge rules, and the `ForgeConfig` struct. Update
> this document whenever you add a new field to `Harness` or `ForgeConfig`,
> move a field between classification tiers, or change merge semantics.
>
> The architectural decisions behind these rules are recorded in
> [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md) (forge-portable
> schema) and [ADR-0088](../ADRs/0088-cel-guarded-overlays.md) (CEL-guarded
> overlays). Those ADRs are point-in-time records; this document reflects the
> current state.

## Field classification

Harness fields are classified into two tiers based on whether they can be
overridden inside `forge.<platform>` blocks or `overlays:` entries.

### Fields that can appear at both levels

These fields can appear at the harness top level (as defaults) and inside
`ForgeConfig` (forge blocks or overlay entries):

| Field              | Rationale                                          |
|--------------------|----------------------------------------------------|
| `pre_script`       | Scripts often call forge-specific CLIs (gh, glab)  |
| `post_script`      | Push, PR/MR creation is forge-specific             |
| `skills`           | Some skills wrap forge-specific APIs               |
| `runner_env`       | Token names and event URLs differ per forge        |
| `validation_loop`  | Validation scripts may call forge-specific tools   |
| `policy`           | Sandbox policies may need forge-specific filesystem or process rules; network access is managed via providers (ADR-0065) but non-network policy sections can still differ per forge |
| `providers`        | Providers may need forge-specific entries (e.g., different API endpoints per platform); concatenated (top-level + forge) |
| `openshell`        | OpenShell profiles may need forge-specific configuration; `profiles` concatenated (top-level + forge) |
| `host_files`       | Host files may need forge-specific entries (e.g., different credential files per platform); deduplicated by `dest` path (child wins) |
| `env`              | Env config (`runner` and `sandbox` sub-maps) may need forge-specific entries (e.g., different token names per forge); sub-maps merged independently, forge/child keys win (ADR-0055) |

### Fields that stay at top level only

These fields are platform-neutral and cannot be overridden per-forge or
per-overlay:

| Field              | Rationale                                          |
|--------------------|----------------------------------------------------|
| `agent`            | Agent definitions are forge-agnostic               |
| `model`            | Model selection is independent of forge             |
| `image`            | Container images are platform-neutral              |
| `api_servers`      | REST proxies abstract forge details                |
| `plugins`          | MCP plugins are forge-agnostic; can be local paths or URLs (ADR-0038) |
| `agent_input`      | Agent prompt input is forge-agnostic               |
| `timeout_minutes`  | Timeouts are operational, not forge-specific        |
| `sandbox_timeout_seconds` | Sandbox-level timeout, not forge-specific   |
| `security`         | Security scanning is forge-agnostic                |
| `allowed_remote_resources` | URL allowlist for resource fetching (ADR 0038) |
| `description`      | Documentation, no runtime effect                   |
| `role`             | Agent identity is forge-agnostic                   |
| `slug`             | Kept top-level; per-forge slug differences handled via `base` composition |
| `base`             | Composition is a structural concern, not forge-specific |
| `doc`              | Documentation path, no runtime effect              |
| `effort`           | Effort level is operational, not forge-specific     |
| `readonly_repo`    | Repo access mode is forge-agnostic                 |
| `allow_runtime_fetch` | Runtime fetch opt-in is forge-agnostic          |
| `max_runtime_fetches` | Fetch cap is operational, not forge-specific     |
| `trigger`          | CEL trigger expression is evaluated against normalized events, not forge-specific (ADR-0061) |

## Merge and inheritance rules

When a forge block or overlay is merged into the harness top level, each
field type follows specific merge semantics. The same rules apply during
`base:` composition (base → child merging).

| Field type       | Merge behavior                                       | Nil vs empty                                          |
|------------------|------------------------------------------------------|-------------------------------------------------------|
| Scalar fields    | Forge/child value overrides top-level/base value     | Absent = inherit from top level / base                |
| `skills`         | Merged with deduplication by basename (forge/child overrides top-level/base) | Absent (nil) = inherit; `skills: []` = empty list merged with base (base entries are returned) |
| `runner_env`     | Top-level/base map merged with forge/child map; forge/child keys win  | Absent (nil) = inherit; `runner_env: {}` = no forge-specific keys (top-level env still inherited) |
| `validation_loop`| Forge/child value replaces top-level/base value entirely | Absent (nil) = inherit from top level / base; explicit empty struct = intended to mean "no validation" (see ADR-0045 open questions) |
| `providers`      | Concatenated (top-level/base + forge/child)           | Absent (nil) = inherit; `providers: []` = no forge-specific additions (top-level providers still apply) |
| `openshell`      | `profiles` concatenated (top-level/base + forge/child) | Absent (nil) = inherit; empty `profiles: []` = no forge-specific additions |
| `host_files`     | Concatenated (base + child); deduplicated by `dest` path (child wins) | Absent (nil) = inherit |
| `plugins`        | Concatenated (base + child)                          | Absent (nil) = inherit |
| `api_servers`    | Concatenated (base + child)                          | Absent (nil) = inherit |
| `env`            | Sub-maps (`runner`, `sandbox`) merged independently; forge/child keys win (ADR-0055) | Absent (nil) = inherit |
| `security`       | Child replaces base entirely (if non-nil)            | Absent (nil) = inherit |
| `overlays` *(planned)* | Concatenated (base + child); first-match-wins at resolution (ADR-0088, not yet implemented) | Absent (nil) = inherit |

## `ForgeConfig` struct

The Go struct that holds per-forge (or per-overlay) configuration:

```go
// ForgeConfig holds platform-specific harness configuration.
// This is purely declarative YAML config — it selects which
// scripts, skills, host files, and env vars to use per platform. It is
// distinct from the forge.Client interface (internal/forge/),
// which is the runtime abstraction for forge API operations.
type ForgeConfig struct {
    PreScript      string            `yaml:"pre_script,omitempty"`
    PostScript     string            `yaml:"post_script,omitempty"`
    Policy         string            `yaml:"policy,omitempty"`
    Skills         []SkillEntry      `yaml:"skills,omitempty"`
    Providers      []string          `yaml:"providers,omitempty"`
    OpenShell      *OpenShellConfig  `yaml:"openshell,omitempty"`
    HostFiles      []HostFile        `yaml:"host_files,omitempty"`
    ValidationLoop *ValidationLoop   `yaml:"validation_loop,omitempty"`
    RunnerEnv      map[string]string `yaml:"runner_env,omitempty"`
    Env            *EnvConfig        `yaml:"env,omitempty"`
}
```

## Current resolution pipeline

The current forge resolution pipeline is:

```
Unmarshal → validateForge → ResolveForge(platform) → Validate
```

## Overlay resolution (ADR-0088)

`overlays:` is the successor to deprecated `forge:` blocks. Each overlay
entry has a `when:` CEL expression and the same override fields as
`ForgeConfig`. The first entry whose `when` evaluates to true is merged;
remaining entries are skipped (first-match-wins).

### Resolution pipeline

```
Unmarshal → validateForge → validateOverlays →
ResolveForge(platform) → ResolveOverlays(event, forgePlatform, config) → Validate
```

When `event` is nil (CLI flows without event context like `fullsend run`
or `fullsend lock`), `ResolveOverlays` substitutes an empty map so
overlays conditioned on `runtime.forge` or `config` can still evaluate
and match. Overlays that reference `event` fields should use `has()` to
guard field access (e.g., `has(event.source) && event.source.system == "jira"`).

### CEL environment

Overlay `when` expressions are evaluated with:

| Variable | Type | Source |
|---|---|---|
| `event` | `normevent.Event` | The triggering event — fields like `source.system`, `entity.kind`, `transition.kind` |
| `runtime.forge` | `string` | Effective forge platform (precedence: CLI flag > config.forge > CI env vars) |
| `config` | `map[string]any` | Full per-repo config from `config.yaml` |

### Mutual exclusion

`forge:` and `overlays:` must not coexist in the same harness (post-merge).
`forge:` is deprecated; new harnesses should use `overlays:` once implemented.

## Related

- [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md): Forge-portable
  harness schema — original architectural decision (Superseded by ADR-0088)
- [ADR-0088](../ADRs/0088-cel-guarded-overlays.md): CEL-guarded overlays —
  current overlay mechanism
- [Harness Composition](harness-composition.md): Merge function checklist
  (step 6 references this document)
- Issue #5579: Harness field integration pipeline (complementary checklist)
