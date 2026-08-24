# Layered Configuration Reference

This guide documents how fullsend resolves per-repo configuration fields
through the layered config system introduced by
[ADR 0069](../../ADRs/0069-ready-made-configuration-presets.md) Decision 2.

For initial setup instructions, see
[Configuring GitHub](../getting-started/configuring-github.md). For advanced
installation variants, see [Advanced Setup](advanced-setup.md).

## Overview

Per-repo configuration is stored in two files inside the target repository's
`.fullsend/` directory:

| File | Role | Writable? |
|------|------|-----------|
| `config.yaml` | **Overlay** — repo-specific customization | Yes |
| `config.base.yaml` | **Base** — vendor preset or shared baseline | Read-through only |

When an accessor reads a config field, it checks the overlay first, then
the base layer, then compiled-in **code defaults**. Only the overlay
(`config.yaml`) is writable — the base layer and code defaults are
read-through layers that the overlay inherits from.

```
config.yaml (overlay)
    ↓ fallthrough when unset
config.base.yaml (base)
    ↓ fallthrough when unset
code defaults (compiled into fullsend)
```

Existing installations without `config.base.yaml` are unaffected — the
overlay falls through directly to code defaults.

### Marshal behavior

`Marshal` (and any serialization path) emits only values explicitly set on
the local layer. Values resolved through a parent layer are never written
back. This means:

- Round-tripping a config through parse → marshal preserves only locally-set
  fields. For slice fields (`roles`, `allowed_remote_resources`), the
  nil-vs-empty distinction is also preserved: an explicitly empty list
  (e.g., `roles: []`) survives the roundtrip and does **not** collapse
  to nil, which would cause unwanted fallthrough to parent defaults.
- Upgrading a base layer (e.g., refreshing `config.base.yaml` from a new
  preset) does not require editing `config.yaml` — the overlay inherits new
  defaults automatically for any field it does not override.

## Unset detection

Each field type uses a zero-value convention to distinguish "unset (fall
through to parent)" from "explicitly set":

| Go type | Unset (falls through) | Set (uses local value) |
|---------|----------------------|------------------------|
| `string` | `""` (empty string) | Any non-empty string |
| `[]T` (slice) | `nil` (key omitted from YAML) | Non-nil, including `[]` (empty slice) |
| `*T` (pointer) | `nil` | Non-nil (including pointer to zero value) |

This distinction matters most for slice and pointer fields, where the
difference between "not specified" and "explicitly empty" drives different
merge behavior.

## Per-field merge rules

The table below documents how each per-repo config field resolves through
the overlay → base → code defaults chain.

| Field | Type | Merge rule | Code default |
|-------|------|------------|--------------|
| `version` | `string` | Scalar override | `"1"` |
| `runtime` | `string` | Scalar override | `"claude"` |
| `kill_switch` | `*bool` | Scalar override | `false` (inactive) |
| `roles` | `[]string` | Replace if set | `PerRepoDefaultRoles()` |
| `agents` | `[]AgentEntry` | Keyed merge by `DerivedName()` | `nil` (none) |
| `allowed_remote_resources` | `[]string` | Union with deny-all | `DefaultAllowedRemoteResources()` |
| `forge` | `string` | Scalar override | `""` (GitHub) |
| `tracker` | `string` | Scalar override | `""` (none) |
| `mint_url` | `string` | Scalar override | `DefaultPerRepoMintURL` (hosted public mint) |
| `inference.provider` | `string` (nested) | Scalar override | `"vertex"` |
| `inference.project` | `string` (nested) | Scalar override | `""` (empty) |
| `inference.region` | `string` (nested) | Scalar override | `"global"` |
| `inference.wif_provider` | `string` (nested) | Scalar override | `""` (empty) |
| `create_issues` | `*CreateIssuesConfig` | Replace whole object if set | `nil` |
| `status_notifications` | `*StatusNotificationConfig` | Replace whole object if set | `nil` |

### Scalar override fields

**`version`**, **`runtime`**, and **`kill_switch`** use simple scalar
override semantics: if the overlay sets the field, that value is used. If
unset, the accessor falls through to the base layer, then to code defaults.

- **`version`**: Schema version string. Unset (`""`) falls through to parent.
  Code default is `"1"`.
- **`runtime`**: Agent runtime identifier. Unset (`""`) falls through to
  parent. Code default is `"claude"`. Valid values: `claude`, `pi`, `dummy`.
- **`kill_switch`**: Pointer to bool (`*bool`). Using a pointer allows
  distinguishing between three states:
  - `nil` (key omitted) — unset, falls through to parent.
  - `*false` (explicit `kill_switch: false`) — locally set to inactive.
    Does **not** fall through.
  - `*true` (explicit `kill_switch: true`) — locally set to active.

### `mint_url` and `inference` — scalar override (ADR 0069 Decision 1)

**`mint_url`** stores the token mint URL. It is a flat string field on
`perRepoConfig` and follows the same scalar override semantics as `runtime`:
unset (`""`) falls through to parent, then to code default
`DefaultPerRepoMintURL` (the hosted public mint).

**`inference`** groups inference backend settings under a single YAML key
(`inference:`) using the `PerRepoInferenceConfig` struct. Each subfield
(`provider`, `project`, `region`, `wif_provider`) resolves independently
through scalar override semantics:

- **`inference.provider`**: Inference provider identifier (e.g. `"vertex"`).
  Unset (`""`) falls through to parent, then to code default `"vertex"`.
- **`inference.project`**: GCP project ID for inference. Unset (`""`) falls
  through to parent (no code default — must be provided by the installer).
- **`inference.region`**: GCP region for inference. Unset (`""`) falls
  through to parent, then to code default `"global"`.
- **`inference.wif_provider`**: Full WIF provider resource name. Unset (`""`)
  falls through to parent (no code default — must be provided by the installer).

The `inference` pointer itself (`*PerRepoInferenceConfig`) uses nil to mean
"no local inference settings" — if the entire `inference:` key is omitted
from YAML, all four subfields fall through to the parent layer. If the key
is present, each subfield is checked independently.

Example:

```yaml
# config.base.yaml (base layer)
mint_url: https://mint.example.com
inference:
  provider: vertex
  project: base-project
  region: us-central1
  wif_provider: projects/123/locations/global/workloadIdentityPools/pool/providers/base

# config.yaml (overlay) — override project only
inference:
  project: my-project
# Effective:
#   mint_url: https://mint.example.com (from base)
#   inference.provider: vertex (from base)
#   inference.project: my-project (from overlay)
#   inference.region: us-central1 (from base)
#   inference.wif_provider: ...base... (from base)
```

### `tracker` — scalar override

**`tracker`** stores the default issue tracker for `fullsend issues`
commands (`github`, `gitlab`, or `jira`). Unset (`""`) means no default
— `--tracker` is required on every `fullsend issues` invocation. When
set, it is used as the default for `--tracker` on both `fullsend issues
get` and `fullsend issues post-comment`; an explicit `--tracker` flag
overrides it. Distinct from `forge`: a repo's hosting forge does not
imply its issue tracker (e.g. a GitHub-hosted repo may track issues in
Jira).

### `roles` — replace if set

The `roles` field uses replace-if-set semantics with **no union**:

- `nil` (key omitted from YAML) — falls through to parent, then to
  code default `PerRepoDefaultRoles()`.
- Non-nil including `roles: []` (explicit empty list) — **replaces** the
  parent value entirely. There is no merge or union of role lists across
  layers. An explicit `roles: []` is preserved through marshal roundtrips
  (it will not be dropped or collapse to nil).

Example:

```yaml
# config.base.yaml (base layer)
roles:
  - triage
  - coder
  - review

# config.yaml (overlay) — replaces, does not union
roles:
  - triage
  - coder
# Effective: [triage, coder] — review is NOT inherited
```

### `agents` — keyed merge by `DerivedName()`

The `agents` field uses keyed merge semantics. Each agent is identified by
its `DerivedName()` — the explicit `name` field if set, otherwise derived
from the source filename (e.g., `triage.yaml` → `triage`).

**Merge behavior:**

- `nil` (key omitted) — parent agents are returned unchanged.
- `agents: []` (explicit empty list) — no overlay entries, but parent
  agents **remain visible**. This is not deny-all.
- Non-empty — overlay entries are merged with parent entries by name:
  - If an overlay entry matches a parent entry by `DerivedName()` (case-
    insensitive), the overlay fields are applied on top of the parent
    entry. Setting `source` in the overlay replaces the parent URL for
    that agent. Setting `enabled` toggles the agent without replacing its
    source.
  - Overlay entries that do not match any parent agent are appended to the
    result.
  - Parent agents with no matching overlay entry pass through unchanged.

Example:

```yaml
# config.base.yaml (base layer)
agents:
  - source: https://example.com/triage.yaml#sha256=abc...
  - source: https://example.com/review.yaml#sha256=def...

# config.yaml (overlay) — disable review, add custom agent
agents:
  - name: review
    enabled: false
  - source: agents/my-custom.yaml
# Effective:
#   - triage.yaml from base (unchanged)
#   - review.yaml from base (disabled by overlay)
#   - my-custom.yaml from overlay (new entry)
```

### `allowed_remote_resources` — union with deny-all

This field controls which URL prefixes are allowed for remote agent sources
and base composition. It uses special three-way semantics:

| Overlay value | Behavior |
|---------------|----------|
| `nil` (key omitted) | Falls through to parent → code defaults |
| `[]` (explicit empty) | **Deny-all** — no remote resources allowed, no fallthrough |
| Non-empty list | **Union** of overlay entries ∪ parent entries |

When the overlay provides a non-empty list and a parent exists, the
effective value is the union of both lists (overlay entries first, then
any parent entries not already present). Code defaults are provided
solely by the terminal `perRepoDefaults` parent — intermediate parents
may return whatever allowlist they want, including omitting the built-in
prefixes. The union does **not** force-append code defaults after the
parent union.

Example:

```yaml
# config.base.yaml (base layer)
allowed_remote_resources:
  - https://raw.githubusercontent.com/fullsend-ai/fullsend/
  - https://raw.githubusercontent.com/fullsend-ai/agents/

# config.yaml (overlay) — adds a custom prefix
allowed_remote_resources:
  - https://raw.githubusercontent.com/my-org/agents/
# Effective (union):
#   - https://raw.githubusercontent.com/my-org/agents/
#   - https://raw.githubusercontent.com/fullsend-ai/fullsend/
#   - https://raw.githubusercontent.com/fullsend-ai/agents/
```

To deny all remote resources (local agents only):

```yaml
# config.yaml (overlay) — explicit empty list
allowed_remote_resources: []
# Effective: [] — no remote resources allowed
```

### `create_issues` — replace whole object if set

The `create_issues` field uses replace-if-set semantics for the entire
object:

- `nil` (key omitted) — falls through to parent, then to code default
  `nil`.
- Non-nil — replaces the parent value entirely. There is no merge of
  `allow_targets` lists across layers.

### `status_notifications` — replace whole object if set

The `status_notifications` field uses the same replace-if-set semantics as
`create_issues`:

- `nil` (key omitted) — falls through to parent, then to code default
  `nil`.
- Non-nil — replaces the parent value entirely, including nested
  `comment.start`/`comment.completion` settings.

## Code defaults reference

When neither the overlay nor the base layer sets a field, the following
compiled-in defaults apply:

| Field | Default value |
|-------|---------------|
| `version` | `"1"` |
| `runtime` | `"claude"` |
| `kill_switch` | `false` (inactive) |
| `roles` | `["triage", "coder", "review", "fix", "retro", "prioritize"]` |
| `agents` | `nil` (none configured) |
| `allowed_remote_resources` | `["https://raw.githubusercontent.com/fullsend-ai/fullsend/", "https://raw.githubusercontent.com/fullsend-ai/agents/"]` |
| `forge` | `""` (GitHub) |
| `tracker` | `""` (none — `--tracker` is required unless set) |
| `mint_url` | `"https://mint.fullsend.sh"` (hosted public mint) |
| `inference.provider` | `"vertex"` |
| `inference.project` | `""` (empty — must be provided) |
| `inference.region` | `"global"` |
| `inference.wif_provider` | `""` (empty — must be provided) |
| `create_issues` | `nil` |
| `status_notifications` | `nil` |

## Related

- [ADR 0069 — Ready-made configuration presets](../../ADRs/0069-ready-made-configuration-presets.md)
  — the architectural decision that introduced layered configuration.
- [ADR 0033 — Per-repo installation mode](../../ADRs/0033-per-repo-installation-mode.md)
  — per-repo config file location and format.
- [Configuring GitHub](../getting-started/configuring-github.md) — initial
  per-repo setup guide.
