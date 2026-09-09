# Harness Composition

When modifying merge functions in the harness package, you must update
**all** counterpart functions that operate on the same set of fields.
These functions form an invariant: they must agree on which harness
fields exist and how each field type is handled. Adding a field to one
function without updating the others silently corrupts harness data
during composition.

## Why this matters

PR #5450 demonstrated the cost of this gap: field-level merge for
`validation_loop` was added to `compose.go` and `forge.go` without
a corresponding update to other merge functions, requiring 6 fix
iterations over 8 days before the PR was closed.

## The invariant

**Any change to a merge function that adds, removes, or changes
field-level handling must be mirrored in the corresponding
merge functions.**

## Paired functions

The following functions must stay in sync. When you modify one, check
and update the others as needed.

### Merge side (harness composition)

| Function | File | Purpose |
|----------|------|---------|
| `mergeBaseIntoChild` | `internal/harness/compose.go` | Merges base harness fields into child during `base:` composition |
| `mergeForgeConfig` | `internal/harness/forge.go` | Applies `forge.<platform>` or overlay overrides onto top-level harness fields |
| `mergeForgeConfigInto` | `internal/harness/compose.go` | **Deprecated** — was used to merge base `ForgeConfig` fields into child `ForgeConfig` during `base:` composition; no longer called after #6798 introduced per-layer resolution |
| `mergeSkills` | `internal/harness/compose.go` | Deduplicates skills by basename (base + child); merges file-level override maps when both define the same basename (child keys win) |
| `mergeHostFiles` | `internal/harness/compose.go` | Deduplicates host files by dest path (base + child) |
| `mergeForgeBlocks` | `internal/harness/compose.go` | **Deprecated** — was used to merge `forge:` maps key-by-key across base and child; no longer called after #6798 introduced per-layer resolution |

> **Note — layer-by-layer resolution.** Since #6798, each base layer's
> forge and overlay blocks are resolved into top-level fields by
> `resolveBaseForgeAndOverlays` **before** the base is merged into the
> child. This means the child's own forge/overlay blocks are never
> mixed with inherited base blocks — they operate independently at
> different stages of the pipeline. Within a single layer, conditional
> values (forge/overlay) override same-layer top-level values
> (specificity). Across layers, child values override inherited base
> values (derivation).

### Validation and resolution side

| Function | File | Purpose |
|----------|------|---------|
| `validateForge` | `internal/harness/forge.go` | Validates `forge:` block keys and `ForgeConfig` field values |
| `validateOverlays` | `internal/harness/forge.go` | Validates `overlays:` entries — CEL `when` expressions and `ForgeConfig` field values; enforces mutual exclusion with `forge:` |
| `ResolveForge` | `internal/harness/forge.go` | Merges the selected forge platform's config into the harness and nils the forge map |
| `ResolveOverlays` | `internal/harness/forge.go` | Evaluates overlay `when` expressions against event/runtime/config CEL environment; merges all matching entries in order (later matches take precedence) and nils the overlays list. When event is nil (CLI flows without event context), an empty map is substituted so overlays conditioned on `runtime.forge` or `config` can still match. Use `has(event.source)` to guard event field access in `when` expressions. |

### How they correspond

The merge functions define which fields participate in harness
composition and how each field type is merged (scalar override, list
append, map merge, struct replace).

For example, if `mergeBaseIntoChild` gains handling for a new
`foo_script` scalar field, then `mergeForgeConfig` must also
handle `foo_script` if it appears inside `ForgeConfig`.

> **Note — resolve before merge.** Each base layer's forge and overlay
> blocks are resolved (flattened into top-level fields) by
> `resolveBaseForgeAndOverlays` in `loadBaseChain` **before** the base
> is merged into the child via `mergeBaseIntoChild`
> ([#6798](https://github.com/fullsend-ai/fullsend/issues/6798)).
> This ensures base forge/overlay values participate in the merge as
> top-level fields, not as forge/overlay blocks that would compete with
> the child's own blocks during the final `ResolveForge`/`ResolveOverlays`.
> When adding a new field to `ForgeConfig`, ensure `mergeForgeConfig`
> handles it (the resolve step delegates to `mergeForgeConfig`).

> **Note — removed counterparts.** Earlier versions of this document
> referenced path-rewriting functions in `internal/cli/migrate.go` and
> diff functions (`DiffHarness`, `diffForgeConfig`) as counterparts to
> the merge functions. The diff functions were removed when ADR-0045
> extracted the scaffold agent. The path-rewriting functions were removed
> with the `migrate-customizations` command (#5864) after the
> `customized/` overlay mechanism was fully deprecated (ADR-0064).

## Checklist for harness field changes

When adding or modifying a field in the `Harness` or `ForgeConfig`
structs:

1. **Determine the field type.** Is it a scalar, list, map, or pointer
   struct? This determines the merge behavior (see
   [Harness Field Reference](harness-fields.md) merge rules).
2. **Update `mergeBaseIntoChild`** if the field participates in `base:`
   composition.
3. **Update `mergeForgeConfig`** if the field can appear under
   `forge.<platform>` blocks.
4. **Update tests** in `compose_test.go` and `forge_test.go` to cover
   the new field in all affected functions.
5. **Update the [Harness Field Reference](harness-fields.md)** — If the
   change adds a new field to `ForgeConfig`, moves a field between
   classification tiers (top-level-only → forge-overridable or vice
   versa), or changes merge semantics, update the relevant tables:
   - Field classification tables ("Fields that can appear at both
     levels" vs "Fields that stay at top level only")
   - Merge and inheritance rules table
   - `ForgeConfig` struct definition

## Checklist for `AgentEntry` field changes

`AgentEntry` is also merged field by field, in
`internal/config/interfaces.go`. A field added to
`internal/config/config.go` is not automatically preserved when a per-repo
configuration overlays a parent configuration.

When adding or modifying a field in `AgentEntry`:

1. **Determine the field's merge semantics.** Document whether an omitted,
   empty, false, or nil value inherits the parent, replaces it, or explicitly
   clears it. Pay particular attention to pointer and map fields.
2. **Update `perRepoConfig.AgentEntries()`** in
   `internal/config/interfaces.go` so matching entries merge the new field.
   Preserve the existing keyed merge by `DerivedName()` and last-overlay-wins
   behavior.
3. **Add layered-merge test coverage** in
   `internal/config/interfaces_test.go` or `internal/config/load_test.go`.
   Test both the overriding value and the inheritance or clearing behavior.
4. **Update the [layered configuration reference](../guides/infrastructure/layered-config-reference.md)**
   with the field's YAML name, type, and merge rule.
5. **Recheck `HasSettings()` and `IsOverrideOnly()`** in
   `internal/config/config.go` when the field affects whether an entry is an
   override-only agent.
6. **Check all other consumers** of the field, including validation,
   serialization, update commands, and runtime readers.

## When reviewing PRs

**When reviewing PRs that touch merge functions:**
Flag any change to a merge function (`compose.go`, `forge.go`) that
adds or modifies field-level handling without a corresponding update to
the other merge functions as a **medium-severity** finding. The fix is
always to update the counterpart function and add test coverage in the
matching `_test.go` file.

## Related

- [Harness Field Reference](harness-fields.md): Living reference for field
  classifications, merge rules, and `ForgeConfig` struct — updated in step 6
- [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md): Forge-portable
  harness schema — original architectural decision (Superseded by ADR-0088)
- [ADR-0088](../ADRs/0088-cel-guarded-overlays.md): CEL-guarded overlays —
  current overlay mechanism
- [ADR-0064](../ADRs/0064-deprecate-customized-directory-overlay.md):
  Deprecate customized directory overlay
- [ADR-0088](../ADRs/0088-cel-guarded-overlays.md): CEL-guarded overlays —
  generalizes forge-specific config with CEL expressions
- Issue #5579: Harness field integration pipeline (complementary
  checklist covering the broader field addition workflow)
