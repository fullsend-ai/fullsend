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
>
> **Decided, not yet implemented (2026-10-01):**
> [ADR 0112](../ADRs/0112-overlays-may-set-any-harness-field.md) will let
> overlays set any field except `base`, `trigger`, `overlays`, `slug`, `role`
> and `forge`, with guarded fields limited to trusted inputs. The tables below
> change when it is implemented.

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
| `plugins`          | Plugin directories are forge-agnostic; each entry is a local path or a pinned URL and keeps its own `env`/`pi` options (ADR-0038, ADR-0094). **Top level only** — not a `ForgeConfig` field, so it is not settable under `forge:` or `overlays:` (a `plugins:` key there is ignored, not an error) |
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
| `privilege_levels` | Mint privilege per run-stage is forge-agnostic (ADR-0073). **Top level only** — not a `ForgeConfig` field |
| `schema_version`   | Schema contract version, forge-agnostic (ADR-0127); absent = version `1`. **Planned, not yet implemented** |
| `preflight_check`  | Single host-dependency gate run once before sandbox creation for `pre_script`/`post_script`/`validation_loop`; a literal `sh -c` command, not a script path (ADR-0128, ADR-0129). **Planned, not yet implemented** |

## Semantic types (ADR 0127)

Each harness field has a semantic type that governs how fullsend interprets
its value ([ADR 0127](../ADRs/0127-harness-schema-versioning-and-field-types.md)).
The following groups cover the top-level fields and their nested values;
`forge.<platform>` and `overlays[]` inherit the types of their shared
`ForgeConfig` fields. A map or list container is structural; its keys and
elements use the types listed below. Ordinary strings, numbers, booleans,
and map values not explicitly identified as paths, commands, or resource
references are scalar values, not shell commands.

| Semantic type | Meaning | Fields |
|---|---|---|
| inline command | Executed via `sh -c` on the host, not resource-resolved | `validation_loop.preflight_check`; top-level `preflight_check` (planned) |
| runtime local path | Path to a file or directory in the local configuration, not a command | `pre_script`, `post_script`, `validation_loop.script`, `validation_loop.schema`, `agent_input` (directory), `host_files[].src` (host path, optional `${VAR}` expansion), `api_servers[].script` (path resolved, server startup planned) |
| resource reference | Local path or pinned URL resolved/fetched as applicable | `agent`, `base`, `policy`, `skills[].source`, `plugins[].path`, `openshell.profiles[]`; `providers[]` has additional identifier semantics below |
| skill override | Key is a path within the skill; value is a local file, pinned URL, or `null` to remove the file | `skills[].overrides[<path>]` |
| source metadata path | Describes a path in the source repository; not runtime-resolved or delivered | `doc` |
| destination path | Names a location inside the sandbox, not a host file to resolve | `host_files[].dest` |
| structural | Contains nested fields, lists, maps, or conditions | `forge`, `overlays[]`, `validation_loop`, `host_files[]`, `api_servers[]`, `skills[]`, `plugins[]`, `plugins[].pi`, `openshell`, `security` and its nested scanner/hook/escalation/trace blocks, `runner_env`, `env`, `env.runner`, `env.sandbox`, `privilege_levels`, `api_servers[].env`, `plugins[].env`, `allowed_remote_resources`, `providers` |
| scalar | Interpreted as a configuration value, never as a path solely because it resembles one | `role`, `slug`, `description`, `image`, `model`, `effort`, `timeout_minutes`, `readonly_repo`, `sandbox_timeout_seconds`, `allow_runtime_fetch`, `max_runtime_fetches`, `trigger` (CEL expression), `schema_version` (planned); `validation_loop.max_iterations`, `validation_loop.feedback_mode`, `host_files[].expand`, `host_files[].optional`, `api_servers[].name`, `api_servers[].port`, `api_servers[].env[<key>]`, `plugins[].env[<key>]`, `plugins[].pi.args[]`, `runner_env[<key>]`, `env.runner[<key>]`, `env.sandbox[<key>]`, `privilege_levels[<stage>]`, `allowed_remote_resources[]`, `overlays[].when` (CEL expression); all leaf values under `security` |

For local harnesses, relative runtime paths resolve from the `.fullsend`
configuration root (the parent of `harness/`), **not** from the YAML file's
directory. The same rule applies to local resource references except a local
`base:`, which resolves relative to the child harness YAML's directory. Direct
resource URLs use the allowlisted, hash-verified fetch pipeline; a URL `base:`
hash verifies the harness YAML, not the relative files fetched alongside it.
Use an immutable commit ref for a URL base whose relative resources will run on
the host. For URL `base:` layers,
relative `pre_script`, `post_script`, `validation_loop.script`/`schema`,
`host_files[].src` (except `${VAR}` sources), `agent`, `policy`, skills
(including overrides), `plugins[].path`, `openshell.profiles[]`, and path-form
`providers[]` are fetched from the base repository and rewritten to cache
paths. An inherited URL-base `agent_input` is **cleared**, because it is a
directory and is not fetched. `doc` is source metadata, not a runtime dependency;
`api_servers[].script` resolves as a local
path, not a URL-base fetch, and server startup is planned. See
[ADR 0038](../ADRs/0038-universal-harness-access.md)
for remote delivery and the [current-field reference](../reference/harness-reference.md)
for implementation status.

`providers[]` is a union: each entry is either a bare identifier (matching
`^[a-zA-Z0-9_-]+$`, looked up under `providers/`) or a fetched resource (a
local path or a `#sha256=` URL). A `skills[]` entry can be a source string or
a single-key map of that source to file overrides; a `plugins[]` entry can be
a path string or a `{path, env, pi}` map. Scalar security leaf values retain
their own validation and defaults; this type table does not override them.

The planned `schema_version` field (absent = `1` once implemented) will declare
this contract; an incompatible field-type change will require a version bump
and an update to this table in the same change. Backward-compatible field
additions do not require a bump; [ADR 0127](../ADRs/0127-harness-schema-versioning-and-field-types.md)
leaves other breaking schema changes for a separate versioning policy. Once
version-aware loaders are implemented, they will reject malformed or
unsupported versions in each raw composition layer before merging; older
pinned consumers must be upgraded before harness content using a new version
is published to them.

## Merge and inheritance rules

When a forge block or overlay is merged into the harness top level, each
field type follows specific merge semantics. The same rules apply during
`base:` composition (base → child merging).

Two independent precedence axes govern field resolution
(see [#6798](https://github.com/fullsend-ai/fullsend/issues/6798)):

- **Specificity (within a layer):** Conditional forge/overlay values
  override same-layer top-level values.
- **Derivation (across layers):** Child-layer values override inherited
  base-layer values. Each base layer's forge and overlay blocks are
  resolved into top-level fields before merging into the child, so
  inherited conditional values cannot override the child's explicit
  settings.

| Field type       | Merge behavior                                       | Nil vs empty                                          |
|------------------|------------------------------------------------------|-------------------------------------------------------|
| Scalar fields    | Forge/child value overrides top-level/base value     | Absent = inherit from top level / base                |
| `skills`         | Merged with deduplication by basename (forge/child overrides top-level/base) | Absent (nil) = inherit; `skills: []` = empty list merged with base (base entries are returned) |
| `runner_env`     | Top-level/base map merged with forge/child map; forge/child keys win  | Absent (nil) = inherit; `runner_env: {}` = no forge-specific keys (top-level env still inherited) |
| `privilege_levels` | Top-level/base map merged with child map; child keys win (not in `ForgeConfig`, so no forge/overlay override) | Absent (nil) = inherit from base; omitted entirely at every layer defaults every run-stage to `write` |
| `validation_loop`| Field-level merge; forge/child non-zero values win, base/top-level fills gaps | Absent (nil) = inherit from top level / base; `validation_loop: {}` inherits all fields (zero-value-as-unset). Post-merge `Validate()` still requires `script`. There is no way to disable an inherited validation loop. |
| `providers`      | Concatenated (top-level/base + forge/child)           | Absent (nil) = inherit; `providers: []` = no forge-specific additions (top-level providers still apply) |
| `openshell`      | `profiles` concatenated (top-level/base + forge/child) | Absent (nil) = inherit; empty `profiles: []` = no forge-specific additions |
| `host_files`     | Concatenated (base + child); deduplicated by `dest` path (child wins) | Absent (nil) = inherit |
| `plugins`        | Concatenated (base + child)                          | Absent (nil) = inherit |
| `api_servers`    | Concatenated (base + child)                          | Absent (nil) = inherit |
| `env`            | Sub-maps (`runner`, `sandbox`) merged independently; forge/child keys win (ADR-0055) | Absent (nil) = inherit |
| `security`       | Child replaces base entirely (if non-nil)            | Absent (nil) = inherit |
| `overlays`       | Concatenated (base + child); all matching entries merged at resolution with later precedence (ADR-0088) | Absent (nil) = inherit |

## `ForgeConfig` struct

`ForgeConfig` is the shared field payload used by both legacy `forge:`
platform blocks and current `overlays:` entries (via `OverlayEntry`'s
`yaml:",inline"` embedding). The type name is a legacy artifact from the
original forge feature (ADR-0045); it was retained when ADR-0088
introduced overlays to avoid a rename-heavy migration. Both mechanisms
use `mergeForgeConfig` to apply their fields onto harness top-level values.

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
`ForgeConfig`. All entries whose `when` evaluates to true are merged
in order, with later matches taking precedence over earlier matches.

### Resolution pipeline

```
Unmarshal → validateForge → validateOverlays →
ResolveForge(platform) → ResolveOverlays(event, forgePlatform, config) → Validate
```

When `event` is nil (CLI flows without event context, such as
`fullsend lock` or `fullsend run` when no event can be recovered),
`ResolveOverlays` substitutes an empty map so
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
`forge:` is deprecated; new harnesses should use `overlays:` instead.

## Related

- [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md): Forge-portable
  harness schema — original architectural decision (Superseded by ADR-0088)
- [ADR-0088](../ADRs/0088-cel-guarded-overlays.md): CEL-guarded overlays —
  current overlay mechanism
- [ADR-0127](../ADRs/0127-harness-schema-versioning-and-field-types.md): Harness
  schema versioning and field semantic types
- [Harness Composition](harness-composition.md): Merge function checklist
  (step 6 references this document)
- Issue #5579: Harness field integration pipeline (complementary checklist)
