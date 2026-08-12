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
| `mergeForgeConfig` | `internal/harness/forge.go` | Applies `forge.<platform>` overrides onto top-level harness fields |
| `mergeForgeConfigInto` | `internal/harness/compose.go` | Merges base `ForgeConfig` fields into child `ForgeConfig` during `base:` composition |
| `mergeSkills` | `internal/harness/compose.go` | Deduplicates skills by basename (base + child) |
| `mergeHostFiles` | `internal/harness/compose.go` | Deduplicates host files by dest path (base + child) |
| `mergeForgeBlocks` | `internal/harness/compose.go` | Merges `forge:` maps key-by-key across base and child |

### How they correspond

The merge functions define which fields participate in harness
composition and how each field type is merged (scalar override, list
append, map merge, struct replace).

For example, if `mergeBaseIntoChild` gains handling for a new
`foo_script` scalar field, then `mergeForgeConfigInto` must also
handle `foo_script` if it appears inside `ForgeConfig`.

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
   struct? This determines the merge behavior (see ADR-0045 inheritance
   rules).
2. **Update `mergeBaseIntoChild`** if the field participates in `base:`
   composition.
3. **Update `mergeForgeConfig`** if the field can appear under
   `forge.<platform>` blocks.
4. **Update `mergeForgeConfigInto`** if the field appears in
   `ForgeConfig` and participates in `base:` composition of forge
   blocks.
5. **Update tests** in `compose_test.go` and `forge_test.go` to cover
   the new field in all affected functions.
6. **Update ADR-0045** — If the change adds a new field to `ForgeConfig`, moves
   a field between classification tiers (top-level-only → forge-overridable),
   or changes merge semantics, update the relevant tables in
   `docs/ADRs/0045-forge-portable-harness-schema.md`:
   - Field classification table ("Fields that stay at top level only" vs
     "Fields that can appear at both levels")
   - `ForgeConfig` struct definition
   - Merge-rules table

## When reviewing PRs

**When reviewing PRs that touch merge functions:**
Flag any change to a merge function (`compose.go`, `forge.go`) that
adds or modifies field-level handling without a corresponding update to
the other merge functions as a **medium-severity** finding. The fix is
always to update the counterpart function and add test coverage in the
matching `_test.go` file.

## Related

- [ADR-0045](../ADRs/0045-forge-portable-harness-schema.md): Forge-portable
  harness schema — defines the merge/inheritance rules
- [ADR-0064](../ADRs/0064-deprecate-customized-directory-overlay.md):
  Deprecate customized directory overlay
- Issue #5579: Harness field integration pipeline (complementary
  checklist covering the broader field addition workflow)
