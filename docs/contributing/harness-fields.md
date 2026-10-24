# Harness Field Reference

> **This is a living document.** It is the authoritative reference for harness
> field classifications, merge rules, and the `ForgeConfig` struct. Update
> this document whenever you add a new field to `Harness` or `ForgeConfig`,
> move a field between classification tiers, or change merge semantics.
>
> The architectural decisions behind these rules are recorded in
> [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md) (forge-portable
> schema), [ADR-0088](../ADRs/0088-cel-guarded-overlays.md) (CEL-guarded
> overlays) and [ADR 0112](../ADRs/0112-overlays-may-set-any-harness-field.md)
> (overlays may set any harness field under a trusted-field guard). Those
> ADRs are point-in-time records; this document reflects the current state.

## Field classification

Harness fields are classified into three classification tiers by where they
may appear: in both the deprecated `forge.<platform>` blocks and `overlays:`
entries, in `overlays:` entries only, or at the top level only.

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

### Fields settable in `overlays:` but not `forge:`

These fields are platform-neutral, so the deprecated `forge.<platform>`
blocks cannot set them, but an `overlays:` entry can (`OverlayFields`,
[ADR 0112](../ADRs/0112-overlays-may-set-any-harness-field.md) Decision 1).
A matching overlay merges them the way a child layer merges into its base
(see [Merge and inheritance rules](#merge-and-inheritance-rules)); an
overlay never clears a value, except that a `security:` block, even an empty
one, replaces the whole top-level block as a child's does. Every one of them
except `model`, `effort`, `description` and `doc` is a
[guarded field](#guarded-fields).

| Field              | Notes                                              |
|--------------------|----------------------------------------------------|
| `agent`            | Replaces the top-level agent. Not settable in an overlay of a harness fetched from a URL: overlay paths are not fetched from the source yet |
| `model`            | Unguarded: changes how well or how cheaply an agent works, not what it may do |
| `effort`           | Unguarded                                          |
| `description`      | Unguarded; documentation, no runtime effect        |
| `doc`              | Unguarded; documentation path, no runtime effect   |
| `image`            | Replaces                                           |
| `api_servers`      | Concatenated (top level + overlay). Not settable in an overlay of a URL-fetched harness yet |
| `plugins`          | Concatenated (top level + overlay); each entry keeps its own `env`/`pi` options (ADR-0038, ADR-0094). Not settable in an overlay of a URL-fetched harness yet |
| `agent_input`      | Replaces. Not settable in an overlay of a URL-fetched harness yet |
| `timeout_minutes`  | A non-zero overlay value replaces                  |
| `sandbox_timeout_seconds` | A non-zero overlay value replaces           |
| `security`         | A non-nil overlay block replaces the whole top-level block |
| `readonly_repo`    | `true` in the overlay turns it on                  |
| `allow_runtime_fetch` | `true` in the overlay turns it on               |
| `max_runtime_fetches` | A set overlay value replaces                    |
| `allowed_remote_resources` | Concatenated (top level + overlay) (ADR 0038). Each entry an overlay adds must lie within the config-level `allowed_remote_resources` (`.fullsend/config.yaml`, merged with `config.base.yaml` and the defaults), or validation fails naming the overlay index and the entry, because runtime fetches are authorized against the merged list. So an overlay's entries never authorize a URL the config-level list does not |
| `privilege_levels` | Map merged, overlay keys win (ADR-0073)            |

Within one harness these values are the harness author's, so an overlay may
add to `allowed_remote_resources`, within the config-level allowlist, or turn
on `allow_runtime_fetch`, which a child never inherits from a `base:`. That
includes a base's own overlays: an overlay in a base layer that sets one of
the three fetch fields is validated, but the value does not reach the child,
so set fetch grants in the harness that runs.

### Fields that stay at top level only

These fields decide how a harness is found, composed or dispatched and
cannot be set in a `forge:` block or an `overlays:` entry. Naming `base`,
`trigger`, `overlays`, `slug`, `role` or `forge` in an overlay fails
validation with the overlay index and the key, including through a YAML
merge key, an alias key or a tagged key. `schema_version` and
`preflight_check` below are planned; the change that implements each places
it in `ForgeConfig`, `OverlayFields` or this set
(`TestOverlayFields_EveryHarnessFieldHasOneHome` fails until it does):

| Field              | Rationale                                          |
|--------------------|----------------------------------------------------|
| `role`             | Agent identity decides dispatch authorization      |
| `slug`             | Kept top-level; per-forge slug differences handled via `base` composition |
| `base`             | Composition is a structural concern                |
| `trigger`          | CEL trigger expression is evaluated against normalized events before any overlay (ADR-0061) |
| `overlays`         | The conditional blocks themselves (ADR 0088)       |
| `forge`            | The deprecated per-platform conditional block; migrate to `overlays` (ADR 0088) |
| `schema_version`   | Schema contract version, forge-agnostic (ADR-0127); absent = version `1`. **Planned, not yet implemented** |
| `preflight_check`  | Single host-dependency gate run once before sandbox creation for `pre_script`/`post_script`/`validation_loop`; a literal `sh -c` command, not a script path (ADR-0128, ADR-0129). **Planned, not yet implemented** ([#8722](https://github.com/fullsend-ai/fullsend/issues/8722)) |

### Guarded fields

Guarding stops a change's author from choosing which overlay sets a field
([ADR 0112](../ADRs/0112-overlays-may-set-any-harness-field.md) Decision 2).
Every overlay-settable field, in `ForgeConfig` and `OverlayFields` alike and
including any field added later, is guarded except `model`, `effort`,
`description` and `doc`. An overlay that sets a guarded field may read in
its `when:` only:

- `runtime.forge` and `config`;
- the event fields the normalized event spec lists as
  [trusted](../normative/normalized-event/v1/README.md#trusted-fields):
  `event.repo`, `event.source.system`, `event.entity.source.system`,
  `event.actor.role`, `event.state.change_proposal.base_repo` and
  `event.state.change_proposal.is_fork` (`event.entity.source.system` is
  reserved for the entity context of ADR 0098; no v1 event carries it, so
  reading it is a non-match);
- `event == null` and `event != null`, and `has()` on a trusted field or
  on a top-level member the schema always requires (`event.repo`,
  `event.entity`, `event.transition`, `event.actor`, `event.state`,
  `event.source`). `has()` on a trusted field under an object that may be
  absent, such as `has(event.state.change_proposal.is_fork)`, matches only
  on events that carry that object, like a read of the field itself, and
  its negation `!has(...)` never matches, so it cannot select runs without
  a change proposal. An overlay that loosens a guarded value on a change
  proposal should also require `!event.state.change_proposal.is_fork`.

The check walks the compiled CEL expression, so reading a parent object
(`event.entity["title"]`, `size(event.state)`,
`event.state.change_proposal != null`), a computed operand
(`dyn(event.entity).title`) or a comprehension over an object is refused
like a direct read of an untrusted field. Validation fails naming the
overlay index, the guarded field and the offending reference. To vary a
guarded field on anything else (the transition, labels, the comment,
`entity.kind`), compose a separate harness through `base:` with its own
`trigger` (ADR 0112, Consequences). On GitHub a built-in agent's stage
still runs its own harness, and a harness `trigger:` never routes a built-in
name, so the separate harness needs another name and runs in addition to
the built-in agent; keep a restriction a built-in agent must always have at
the top level. The separate harness also does not inherit the base's
`readonly_repo` ([#7360](https://github.com/fullsend-ai/fullsend/issues/7360)),
so set it there again. The
[ADR 0136](../ADRs/0136-runner-secrets-through-one-stored-secret.md)
runner-secret check runs first at every `LoadWithBase` load site, but only
`fullsend run` passes it the runner-secret names, so it refuses only there:
an overlay whose env refers to a runner secret may be guarded only by
`runtime.forge` and `config`.

## Semantic types (ADR 0127)

Each harness field has a semantic type that governs how fullsend interprets
its value ([ADR 0127](../ADRs/0127-harness-schema-versioning-and-field-types.md)).
The following groups cover the top-level fields and their nested values;
`forge.<platform>` blocks inherit the types of their `ForgeConfig` fields,
and `overlays[]` entries those of the `ForgeConfig` and `OverlayFields`
fields they set. A map or list container is structural; its keys and
elements use the types listed below. Ordinary strings, numbers, booleans,
and map values not explicitly identified as paths, commands, or resource
references are scalar values, not shell commands.

| Semantic type | Meaning | Fields |
|---|---|---|
| inline command | Executed via `sh -c` on the host against the allowlisted preflight environment (credential-safe), not resource-resolved | `validation_loop.preflight_check`; top-level `preflight_check` (planned) |
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
`base:` composition (base → child merging). A matching `overlays:` entry's
`OverlayFields` follow the same rows with the overlay in the forge/child
position (`mergeOverlayFields` mirrors `mergeBaseIntoChild`), except that
`allowed_remote_resources`, `allow_runtime_fetch`, `max_runtime_fetches` and
`readonly_repo`, which a child never inherits from a base, are applied from
the overlay (concatenated, turned on, replaced and turned on respectively;
`readonly_repo` not being inherited is
[#7360](https://github.com/fullsend-ai/fullsend/issues/7360)).

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
| `skills`         | Merged with deduplication by skill identity (declared `SKILL.md` name, otherwise directory basename; `skill.SandboxName`). Relative entries are resolved against the fullsend directory before the merge, so a local override matches by declared name too. Two entries with one identity in a single list are both kept and fail at bootstrap. A relative entry reached through a symlink that leaves the fullsend directory, or that has an absolute target, at any component including the marker file, keeps its directory basename as its merge identity. Forge/child overrides a matching top-level/base entry. Sibling nested dirs that share a leaf name are kept. | Absent (nil) = inherit; `skills: []` = empty list merged with base (base entries are returned) |
| `runner_env`     | Top-level/base map merged with forge/child map; forge/child keys win  | Absent (nil) = inherit; `runner_env: {}` = no forge-specific keys (top-level env still inherited) |
| `privilege_levels` | Top-level/base map merged with child map; child keys win (not in `ForgeConfig`, so no `forge:` override; an `overlays:` entry merges its map the same way, overlay keys win) | Absent (nil) = inherit from base; omitted entirely at every layer defaults every run-stage to `write` |
| `validation_loop`| Field-level merge; forge/child non-zero values win, base/top-level fills gaps | Absent (nil) = inherit from top level / base; `validation_loop: {}` inherits all fields (zero-value-as-unset). Post-merge `Validate()` still requires `script`. There is no way to disable an inherited validation loop. |
| `providers`      | Concatenated (top-level/base + forge/child)           | Absent (nil) = inherit; `providers: []` = no forge-specific additions (top-level providers still apply) |
| `openshell`      | `profiles` concatenated (top-level/base + forge/child) | Absent (nil) = inherit; empty `profiles: []` = no forge-specific additions |
| `host_files`     | Concatenated (base + child); deduplicated by `dest` path (child wins) | Absent (nil) = inherit |
| `plugins`        | Concatenated (base + child)                          | Absent (nil) = inherit |
| `api_servers`    | Concatenated (base + child)                          | Absent (nil) = inherit |
| `env`            | Sub-maps (`runner`, `sandbox`) merged independently; forge/child keys win (ADR-0055) | Absent (nil) = inherit |
| `security`       | Child replaces base entirely (if non-nil)            | Absent (nil) = inherit |
| `overlays`       | Not merged: each layer resolves its own before the merge ([#6798](https://github.com/fullsend-ai/fullsend/issues/6798)); within a layer all matching entries are merged with later precedence (ADR-0088) | — |

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

`OverlayEntry` also inlines `OverlayFields`, the fields an overlay may set
beyond `ForgeConfig` ([field classification](#field-classification));
`ResolveOverlays` applies them with `mergeOverlayFields`, which must stay
in step with `mergeBaseIntoChild`
([Harness Composition](harness-composition.md)).
Together, `ForgeConfig`, `OverlayFields` and the excluded keys (`base`,
`trigger`, `overlays`, `slug`, `role`, `forge`) cover every `Harness` YAML
key; `TestOverlayFields_EveryHarnessFieldHasOneHome` fails when a new
`Harness` field is placed in none of them.

## Current resolution pipeline

The current forge resolution pipeline is:

```
Unmarshal → validateForge → ResolveForge(platform) → Validate
```

## Overlay resolution (ADR-0088)

`overlays:` is the successor to deprecated `forge:` blocks. Each overlay
entry has a `when:` CEL expression and may set any harness field except
`base`, `trigger`, `overlays`, `slug`, `role` and `forge`
([field classification](#field-classification)). All entries whose `when`
evaluates to true are merged in order, with later matches taking precedence
over earlier matches.

### Resolution pipeline

```
Unmarshal → validateForge → validateOverlays →
ResolveForge(platform) → ResolveOverlays(event, forgePlatform, config) → Validate
```

`validateOverlays` rejects an excluded key, compiles each `when:` to a
bool, refuses a guarded field whose `when:` reads anything but trusted
facts ([Guarded fields](#guarded-fields)), refuses an overlay
`allowed_remote_resources` entry the config-level allowlist does not cover,
and checks every entry's values with the top-level checks
(`validateOverlaySettable`) whether or not its `when:` matches, so a
malformed value in a never-matching overlay fails the load when the
top-level values are valid. The rules that span fields
(`allow_runtime_fetch` needs a list, `max_runtime_fetches` needs
`allow_runtime_fetch`) are checked only on the resolved harness, since a
later matching entry may complete them. The ADR 0136 runner-secret check
runs just before `validateOverlays` at every `LoadWithBase` load site; it
refuses only in `fullsend run`, the one caller that passes runner-secret
names.

`ResolveOverlays` classifies an evaluation error by context:

| Context | Error | Result |
|---|---|---|
| No event (`fullsend lock`, dispatch enumeration, `fullsend run` when no event can be recovered) | any | non-match; the entry is skipped |
| Event present | missing key (`no such key`) | non-match |
| Event present, entry sets a guarded field | any other | the run fails |
| Event present, entry sets only unguarded fields | any other | logged, non-match |

When `event` is nil, `when:` sees it as CEL `null`: overlays conditioned on
`runtime.forge` or `config` still evaluate and match, `event != null` is
false and `event == null` is true, and `has(event.x)` is false. Selecting an
event field (`event.source.system`) or using `event` as a map (`size(event)`,
`"k" in event`) is an evaluation error and so a non-match, and a comparison
with a map changes result (`event != {}` is true).

### CEL environment

Overlay `when` expressions are evaluated with:

| Variable | Type | Source |
|---|---|---|
| `event` | `normevent.Event` | The triggering event — fields like `source.system`, `entity.kind`, `transition.kind`. The ADR 0107 fields `actor.bot_role` and `actor.role_verified` are present only when an adapter performed a bot-role lookup; guard them with `has()` in `trigger:` or in an unguarded overlay (a guarded overlay may not probe them) |
| `runtime.forge` | `string` | Effective forge platform (precedence: CLI flag > config.forge > CI env vars) |
| `config` | `map[string]any` | Full per-repo config from `config.yaml` |

### Mutual exclusion

`forge:` and `overlays:` must not coexist in the same harness (post-merge).
`forge:` is deprecated; new harnesses should use `overlays:` instead.

## Event payload and normalized-event resolution

During dispatch, fullsend projects normalized events (`internal/normevent/`) into
`event_payload` via `buildEventPayload` in `internal/harnessdispatch/project.go`.
This payload provides backwards-compatible legacy forge fields (`issue`,
`pull_request`, `comment`) alongside `_normalized_event` (the serialized
`normevent.Event` map). `fullsend run` stops when the payload is not JSON
or `_normalized_event` is present but fails validation, rather than
resolving overlays as if there were no event.

When resolving CEL-guarded overlays via `ResolveOverlays(event, forgePlatform,
config)`, the runtime passes the deserialized `normevent.Event` directly as
the `event` variable in the CEL environment (see [CEL environment](#cel-environment)
above). When adding, modifying, or removing fields in normalized event structures
or `event_payload`:

1. Update `buildEventPayload` in `internal/harnessdispatch/project.go` and its
   unit tests in `internal/harnessdispatch/project_test.go`.
2. Update the projection table in `docs/normative/normalized-event/v1/README.md`.
3. If new event fields or properties are exposed to overlay CEL evaluation,
   document them in the [CEL environment](#cel-environment) table above.

## Related

- [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md): Forge-portable
  harness schema — original architectural decision (Superseded by ADR-0088)
- [ADR-0088](../ADRs/0088-cel-guarded-overlays.md): CEL-guarded overlays —
  current overlay mechanism
- [ADR 0112](../ADRs/0112-overlays-may-set-any-harness-field.md): Overlays
  may set any harness field; guarded fields read only trusted facts
- [ADR-0127](../ADRs/0127-harness-schema-versioning-and-field-types.md): Harness
  schema versioning and field semantic types
- [Harness Composition](harness-composition.md): Merge function checklist
  (step 6 references this document)
- Issue #5579: Harness field integration pipeline (complementary checklist)
