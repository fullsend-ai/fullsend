# Harness Field Integration Checklist

When adding or modifying fields in harness schema structs (`Harness`,
`ValidationLoop`, `ForgeConfig`, or `EnvConfig` in
`internal/harness/harness.go` and `internal/harness/forge.go`), the new
field must be wired into every pipeline stage that sibling fields
already pass through. This checklist covers the four stages. Trace an
existing field with similar semantics (e.g., `Schema` for declarative
paths, `PreScript` for executable paths, `RunnerEnv` for env maps)
through each stage to see the pattern.

## 1. Expansion pipeline — `${VAR}` substitution

Fields whose values may contain `${VAR}` references must be routed
through two steps in `internal/cli/run.go`:

1. **Validation** — register the field in `ValidateRunnerEnvWith` (in
   `harness.go`) by calling `checkVarRefs` so unset host variables are
   caught at startup, not at runtime.
2. **Expansion** — call `os.Expand(value, expander)` in `run.go`'s
   harness boot sequence (the block after `expander` is defined) so
   `${VAR}` references resolve to actual values before downstream
   consumers read the field.

**Fields currently wired through this pipeline:**

| Field | `checkVarRefs` | `os.Expand` |
|-------|---------------|-------------|
| `RunnerEnv[k]` (all values) | ✓ | ✓ |
| `Env.Runner[k]` | ✓ | ✓ |
| `Env.Sandbox[k]` | ✓ | ✓ |
| `HostFiles[i].Src` | ✓ (skip if `Optional`) | — (expanded later via `os.ExpandEnv`) |
| `ValidationLoop.Schema` | ✓ | ✓ |
| `ValidationLoop.PreflightCheck` | ✓ | ✓ |

**When to wire a new field:** If the field's value is a path, shell
command, or any user-authored string where `${FULLSEND_DIR}` or other
host variables should resolve. Inline shell commands that are executed
directly (e.g., `PreflightCheck`) still need expansion because the
command string itself may reference env vars.

## 2. Environment construction — `RunnerEnv` merge

Host-side commands (pre-script, post-script, preflight check,
validation loop) must see the harness `RunnerEnv` variables. Each call
site uses one of two helpers in `internal/cli/run.go`:

- **`childScriptEnv(h.RunnerEnv, traceparent)`** — merges `RunnerEnv`
  over the process environment plus W3C `TRACEPARENT`. Used for
  pre-script and post-script `exec.Command.Env`.
- **`envToList(h.RunnerEnv)`** — converts the map to `KEY=VALUE`
  strings appended to `os.Environ()` or a custom env slice. Used for
  preflight check and validation loop.

**Call sites currently wired:**

| Command | Env source | Helper |
|---------|-----------|--------|
| Pre-script | `childScriptEnv(h.RunnerEnv, traceparent)` | `childScriptEnv` |
| Post-script | `childScriptEnv(h.RunnerEnv, traceparent)` | `childScriptEnv` |
| Preflight check | `append(os.Environ(), envToList(h.RunnerEnv)...)` | `envToList` |
| Validation loop | `validationEnv(h, ...)` → `envToList(h.RunnerEnv)` | `envToList` |

**When to wire a new field:** If the field introduces a new host-side
`exec.Command`, that command's `Env` must include `RunnerEnv`. Choose
`childScriptEnv` for scripts that need trace propagation, or
`envToList` for simpler probes.

## 3. Composition carry-forward — merge sites

New fields must be carried forward at every merge site so that base
composition and forge resolution do not silently drop inherited values.
There are three merge functions:

### `mergeBaseIntoChild(base, child *Harness)` — `compose.go`

Merges a base harness into a child during `base:` composition. Rules:

- **Scalars** — child overrides if non-zero
- **Slices** (`Skills`, `Plugins`, `Providers`, `APIServers`) —
  concatenated (base + child), some with dedup
- **Maps** (`RunnerEnv`) — merged, child keys win
- **Pointer structs** (`ValidationLoop`, `Security`) — child replaces
  if non-nil; specific sub-fields may carry forward independently
  (e.g., `PreflightCheck` carries forward when child overrides
  `ValidationLoop` without setting its own preflight check)
- **`HostFiles`** — concatenated with last-writer-wins dedup by `Dest`
- **`Forge`** — key-by-key merge via `mergeForgeBlocks`
- **`AllowedRemoteResources`, `AllowRuntimeFetch`,
  `MaxRuntimeFetches`** — NOT merged (security: child must declare
  its own)

### `mergeForgeConfigInto(base, child *ForgeConfig)` — `compose.go`

Merges base forge config into child forge config during base
composition (`mergeForgeBlocks` iterates per-platform and calls this).
Same scalar/slice/map rules as `mergeBaseIntoChild` but scoped to
`ForgeConfig` fields (`PreScript`, `PostScript`, `Skills`, `RunnerEnv`,
`Env`, `ValidationLoop`).

### `mergeForgeConfig(h *Harness, fc *ForgeConfig)` — `forge.go`

Merges platform-specific forge overrides into the top-level harness
during `ResolveForge`. Rules:

- **Scalars** (`PreScript`, `PostScript`) — forge overrides if
  non-empty
- **`Skills`** — top-level + forge (concatenated via `mergeSkills`)
- **`RunnerEnv`** — merged, forge keys win
- **`Env`** — sub-maps merged independently, forge keys win
- **`ValidationLoop`** — forge replaces entirely if non-nil

**When to wire a new field:** Add the field to every merge function
where its parent struct is handled. If the field has sub-fields that
should survive an override (like `PreflightCheck` survives a
`ValidationLoop` override), add explicit carry-forward logic. Decide
whether the field should be merged (maps), concatenated (slices),
scalar-overridden, or excluded (security-sensitive fields).

## 4. Security pipeline — URL-sourced content

Fields that reference external content (files fetched via `base:`
composition or `SourceURL` resolution) must pass through the security
functions in `compose.go`:

- **`resolveBaseScripts`** — fetches executable fields (`PreScript`,
  `PostScript`, `ValidationLoop.Script`) and declarative path fields
  (`ValidationLoop.Schema`) from URL-referenced bases. Each field is
  validated by `validateBaseRelPath` (rejects null bytes, path
  traversal, absolute paths) and fetched via `fetchBaseFile` (checks
  allowlist, verifies integrity hash, caches content-addressed).
  Executable fields are `chmod 0o755` after caching.
- **`resolveBaseResources`** — fetches declarative resource fields
  (`Agent`, `Policy`, `Skills[]`) from URL-referenced bases. Same
  validation and fetch pipeline.
- **`resolveBaseHostFiles`** — fetches `HostFiles[].Src` with relative
  paths from URL-referenced bases. Entries using `${VAR}` expansion
  are skipped (they resolve at bootstrap time on the host).
- **`validateBaseRelPath`** — validates that inherited relative paths
  are safe: no null bytes, no `?` or `#` markers, no `..` traversal,
  no absolute paths, no URLs.
- **`auditBaseFetch`** — appends a JSONL audit log entry for every
  fetch operation, recording URL, SHA256, allowlist match, cache hit,
  and trace ID.

**When to wire a new field:** If the field is a path or content
reference that could originate from a URL-sourced base harness, add it
to the appropriate `resolveBase*` function. Executable fields go in
`resolveBaseScripts`; declarative/data fields go in
`resolveBaseResources` or `resolveBaseHostFiles`. Inline shell commands
(like `PreflightCheck`) that are embedded in the harness YAML — not
external file references — are exempt from fetch resolution but still
need `validateBaseRelPath` if they could contain path references.

## Quick reference: tracing a sibling field

To verify you've covered all stages for a new field, pick the most
similar existing field and grep for every place it appears:

```bash
# Example: trace ValidationLoop.Schema through all 4 stages
grep -rn 'Schema' internal/harness/harness.go internal/harness/compose.go \
  internal/harness/forge.go internal/cli/run.go
```

Check that your new field appears in each matching context:

1. `checkVarRefs` call in `ValidateRunnerEnvWith` (if it supports
   `${VAR}`)
2. `os.Expand` call in `run.go` (if it supports `${VAR}`)
3. All three merge functions (if the field lives in a merged struct)
4. `resolveBase*` function (if it references external content)
