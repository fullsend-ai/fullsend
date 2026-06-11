# Implementation Plan: Phase 2 — Transitive Dependency Resolution

## Context

Phase 1 (MVP) of ADR-0038 is fully shipped (8 PRs merged). It added URL detection, SSRF-hardened fetching, content-addressed caching, audit logging, schema extensions, resource resolution, and CLI integration. Phase 1 treats all URL-referenced resources as **leaf nodes** — a fetched skill cannot itself reference other URL-based resources.

Phase 2 removes this limitation. URL-referenced skills can declare `dependencies:` in their YAML frontmatter, and the resolver will recursively fetch and validate those transitive dependencies. This enables skill composition: a "rust-conventions" skill can depend on a "cargo-integration" skill without requiring the harness author to enumerate every transitive dependency.

Design details are in `docs/plans/universal-harness-access.md` (sections "Transitive Closure", "Relative Path Resolution for URL-Referenced Resources", and "Dependency Graph and Resolution"). The ADR is at `docs/ADRs/0038-universal-harness-access.md`.

> **Scope note:** The design doc envisions transitive resolution for all resource types (agents reference skills, skills reference policies, policies reference schemas). Phase 2 limits transitive resolution to skills only — agent and policy resources are treated as leaf nodes. Extending to other resource types is deferred to a future phase if needed.

## PR Dependency Graph

```
PR 1 (skill frontmatter parser) ──> PR 2 (recursive resolver + relative URL resolution) ──> PR 3 (CLI wiring + flags)
```

PRs are strictly sequential. Each is independently reviewable and safe to merge alone — earlier PRs introduce new code with no callers until the subsequent PR wires them in.

---

## PR 1: Skill frontmatter parser — extract `dependencies:` from SKILL.md

**Scope:** New package `internal/skill/` with a parser that extracts YAML frontmatter from SKILL.md content (bytes, not files). `ParseFrontmatter` reads SKILL.md content from the resolved skill directory (whether local or cached from forge), not from a raw URL response. Pure functions with no callers. Zero risk to existing behavior.

**Rationale for new package:** Skill frontmatter parsing is a distinct concern from harness loading (`internal/harness/`) and resource resolution (`internal/resolve/`). Placing it in its own package avoids circular dependencies: `internal/resolve/` will import `internal/skill/`, but `internal/skill/` imports nothing from the resolve or harness packages.

**Create `internal/skill/skill.go`:**

```go
package skill

// SkillMeta holds parsed YAML frontmatter from a SKILL.md file.
type SkillMeta struct {
    Name         string   `yaml:"name"`
    Description  string   `yaml:"description,omitempty"`
    Dependencies []string `yaml:"dependencies,omitempty"`
}

// ParseFrontmatter extracts the YAML frontmatter block (delimited by "---")
// from SKILL.md content and unmarshals it into SkillMeta. Returns nil (no error)
// if the content has no frontmatter. Returns an error only if frontmatter is
// present but malformed.
//
// Frontmatter format:
//   ---
//   name: rust-conventions
//   dependencies:
//     - ../common/cargo-integration#sha256=<tree-hash>...
//     - https://github.com/fullsend-ai/skills/tree/8cd3799.../security-baseline#sha256=<tree-hash>...
//   ---
func ParseFrontmatter(content []byte) (*SkillMeta, error)
```

Implementation notes:
- Split content on `---` delimiters (first line must be `---`, find second `---`).
- Use `gopkg.in/yaml.v3` to unmarshal the frontmatter block.
- Return `nil` (no error) if the first line is not `---` — this handles plain Markdown skills with no frontmatter.
- The `Dependencies` field holds raw reference strings (URLs or relative paths). Resolution is the caller's responsibility.

**Create `internal/skill/skill_test.go`:**

Test cases:
- **Valid frontmatter with dependencies:** Parse skill with `dependencies:` list, verify all entries extracted.
- **Valid frontmatter without dependencies:** Parse skill with `name:` but no `dependencies:` — returns empty `Dependencies` slice.
- **No frontmatter:** Plain Markdown content (no `---` delimiter) — returns `nil`, no error.
- **Malformed YAML in frontmatter:** Invalid YAML between `---` delimiters — returns error.
- **Mixed URL and relative dependencies:** Verify that raw strings are preserved as-is (no resolution at this stage).
- **Empty frontmatter block:** `---\n---\n` with nothing between — returns `&SkillMeta{}`, no error.
- **Content after frontmatter:** Verify Markdown body after the closing `---` is ignored.
- **Existing SKILL.md format compatibility:** Parse a real SKILL.md from the repo (e.g., `skills/merge-queue/SKILL.md`) — existing fields (`name`, `description`, `allowed-tools`) are parsed without error; existing fields not in `SkillMeta` (like `allowed-tools`, `disable-model-invocation`) are silently ignored by `yaml.v3`'s default behavior (no `KnownFields(true)` is set).

**Depends on:** Nothing

**After merge:** Frontmatter parser available. No callers. All existing tests pass.

---

## PR 2: Recursive resolver with cycle detection and depth/breadth limits

**Scope:** Extends `internal/resolve/resolve.go` to recursively resolve transitive dependencies declared in fetched SKILL.md files. Transitive skill dependencies resolve to directories (same as direct skill dependencies) and use forge API for listing and fetching. The resolver reads SKILL.md from the cached directory's `tree/` subdirectory. Modifies existing resolver internals but does not change the `ResolveHarness` public signature or behavior for harnesses without transitive dependencies. Harnesses with only local paths or single-level URL references continue to work identically.

### Changes to `internal/resolve/resolve.go`

**Add new types:**

```go
// resolveState tracks shared mutable state across recursive resolution calls.
// A single resolveState is created per ResolveHarness invocation and threaded
// through all recursive calls.
//
// Cycle detection uses two-phase DFS tracking to distinguish true cycles
// (A→B→A) from valid DAG diamond patterns (A→B→D, A→C→D). The inProgress
// set tracks URLs on the current call stack; resolved tracks fully processed
// URLs. A URL encountered in inProgress is a cycle; a URL in resolved is a
// diamond and is skipped without error.
type resolveState struct {
    inProgress    map[string]bool       // URL -> true while on the current DFS stack
    resolved      map[string]Dependency // URL -> result, fully processed (skip on re-encounter)
    resourceCount int                   // total resources fetched so far
    deps          []Dependency          // accumulated resolved dependencies
}
```

**Add to `ResolveOpts`:**

```go
type ResolveOpts struct {
    WorkspaceRoot string
    FetchPolicy   fetch.FetchPolicy
    TraceID       string
    AuditLogPath  string
    MaxDepth      int // Maximum recursion depth (default: 10)
    MaxResources  int // Maximum total resources per harness (default: 50)
}
```

Default constants:

```go
const (
    DefaultMaxDepth     = 10
    DefaultMaxResources = 50
)
```

`MaxDepth` and `MaxResources` are passed through directly from the CLI flags (which default to 10 and 50 respectively). Setting `MaxDepth` to 0 disables transitive resolution entirely.

> **Note:** The design doc's pseudocode placed `MaxDepth`/`MaxResources` in `FetchPolicy`. We place them in `ResolveOpts` because they are resolution-time concerns (graph traversal limits), not per-fetch concerns (timeouts, retries, size limits).

**Modify `ResolveHarness`:**

The public signature remains unchanged: `ResolveHarness(ctx, h, opts) ([]Dependency, error)`. Internally, it now creates a `resolveState` and passes it to `resolveURL`, which calls a new `resolveTransitiveDeps` function after fetching each skill.

```go
func ResolveHarness(ctx context.Context, h *harness.Harness, opts ResolveOpts) ([]Dependency, error) {
    state := &resolveState{
        inProgress: make(map[string]bool),
        resolved:   make(map[string]Dependency),
    }
    maxDepth := opts.MaxDepth      // 0 = no transitive resolution
    maxResources := opts.MaxResources

    // Resolve top-level fields (agent, policy, skills) at depth 0.
    // Same logic as Phase 1, but resolveURL now accepts state and depth.
    // ... (existing iteration over h.Agent, h.Policy, h.Skills)

    return state.deps, nil
}
```

**Modify `resolveURL`:**

Add `state *resolveState`, `depth int`, `maxDepth int`, and `maxResources int` parameters. After fetching and caching a skill URL, call `resolveTransitiveDeps`.

```go
func resolveURL(ctx context.Context, field, rawURL string, h *harness.Harness,
    opts ResolveOpts, state *resolveState, depth, maxDepth, maxResources int) (Dependency, string, error)
```

Key changes inside `resolveURL`:
1. **Already resolved (diamond) check:** If `state.resolved[cleanURL]` exists, return the cached `Dependency` immediately — no fetch, no budget consumed. This handles DAG diamonds (A→B→D, A→C→D) efficiently.
2. **Cycle detection:** Check `state.inProgress[cleanURL]`. If true, return error: `"circular dependency detected: %s"`. This catches true cycles (A→B→A) because `inProgress` tracks the current DFS call stack.
3. **Breadth check:** Verify `state.resourceCount < maxResources`. If exceeded, return error: `"exceeded maximum resource count of %d"`. This runs after the diamond/cycle checks so that revisited URLs do not consume budget.
4. **Mark in-progress and increment:** Set `state.inProgress[cleanURL] = true` and `state.resourceCount++`.
5. **Guard on resource type:** Only call `resolveTransitiveDeps` for skill-type resources. Check `strings.HasPrefix(field, "skills")` before calling. Agent and policy resources skip this step — their content may contain `---` delimiters (especially YAML policy files) that `ParseFrontmatter` would misinterpret.
6. After successful fetch/cache (for skills only), call `resolveTransitiveDeps(ctx, cleanURL, content, h, opts, state, depth, maxDepth, maxResources)`.
7. **Mark resolved:** After resolution completes, delete from `state.inProgress` and store the result in `state.resolved[cleanURL]`.

**Add `resolveTransitiveDeps`:**

```go
// resolveTransitiveDeps parses a fetched skill's frontmatter for dependencies
// and recursively resolves them. Only called for skill-type resources (SKILL.md).
// Agent and policy resources are leaf nodes and do not declare dependencies.
func resolveTransitiveDeps(ctx context.Context, parentURL string, content []byte,
    h *harness.Harness, opts ResolveOpts,
    state *resolveState, depth, maxDepth, maxResources int) error
```

Logic:
1. **Depth check:** If `depth+1 > maxDepth`, return error: `"exceeded maximum dependency depth of %d at %s"`.
2. **Read SKILL.md from cached directory:** For directory skills, read `SKILL.md` from the cached directory's `tree/` subdirectory (e.g., `.fullsend-cache/resources/sha256/<hash>/tree/SKILL.md`).
3. **Parse frontmatter:** Call `skill.ParseFrontmatter(content)`. If error, return wrapped error. If `nil` or no dependencies, return nil (leaf node).
4. **Resolve each dependency reference:**
   - If the reference is an absolute URL (`harness.IsURL(ref)`): use as-is.
   - If the reference is a relative path: resolve relative to `parentURL` using `ResolveRelativeURL(parentURL, ref)` (defined in `relurl.go` below). For directory skills, relative paths resolve to sibling directories, not sibling files (e.g., `../common/formatting` resolves to a sibling directory).
   - Recursively call `resolveURL(ctx, field, resolvedRef, h, opts, state, depth+1, maxDepth, maxResources)`.
**Backward compatibility:** For harnesses with no URL-referenced skills, `resolveTransitiveDeps` is never called. For URL-referenced skills whose content has no `dependencies:` frontmatter, `ParseFrontmatter` returns `nil` and the function returns immediately. Phase 1 behavior is preserved exactly.

### New file: `internal/resolve/relurl.go`

```go
// ResolveRelativeURL resolves a relative reference against a parent URL's
// base path, following RFC 3986 semantics. The parent URL is the URL from
// which the containing resource was fetched.
//
// Examples:
//   ResolveRelativeURL("https://github.com/org/skills/tree/abc123/rust", "../common/formatting")
//   → "https://github.com/org/skills/tree/abc123/common/formatting"
//
//   ResolveRelativeURL("https://github.com/org/skills/tree/abc123/rust", "policies/sandbox.yaml")
//   → "https://github.com/org/skills/tree/abc123/rust/policies/sandbox.yaml"
//
// Security: The resolved URL is returned as-is. The caller must validate it
// against allowed_remote_resources prefixes (which operates on the normalized
// URL, preventing path traversal attacks like ../../../../attacker-org/evil).
func ResolveRelativeURL(parentURL, relRef string) (string, error)
```

Implementation:
1. Parse `parentURL` with `net/url.Parse`.
2. Parse `relRef` with `net/url.Parse`.
3. Use `parent.ResolveReference(rel)` (Go's `net/url` implements RFC 3986 reference resolution, including `..` normalization).
4. Return `resolved.String()`.

This is deliberately simple — the security boundary is enforced by the existing `MatchingAllowedPrefix` check in `resolveURL`, which operates on the fully resolved and normalized URL. Path traversal via `../../../attacker-org/` is caught there, not here.

### New file: `internal/resolve/relurl_test.go`

Test cases:
- **Sibling directory reference:** `../common/formatting` relative to `.../tree/abc123/skills/rust` resolves to `.../tree/abc123/skills/common/formatting`.
- **Child reference:** `policies/sandbox.yaml` relative to `.../tree/abc123/skills/rust` resolves to `.../tree/abc123/skills/rust/policies/sandbox.yaml`.
- **Absolute URL reference:** `https://github.com/other-org/skills/tree/abc123/python` is returned unchanged (no resolution against parent).
- **Path traversal:** `../../../../attacker/evil` relative to `.../org/skills/tree/abc123/rust` resolves to `https://github.com/attacker/evil` (valid URL — the caller's prefix check rejects it).
- **Multiple `..` segments:** `../../other/sub/formatting` resolves correctly.
- **Fragment preservation:** `../common/formatting#sha256=<tree-hash>` resolves with the `#sha256=...` fragment intact. Integrity checking depends on the fragment surviving `url.ResolveReference`.
- **Trailing slash handling:** Parent URL with and without trailing slash.

### Updates to `internal/resolve/resolve_test.go`

New test cases (in addition to existing Phase 1 tests, which remain unchanged):

- **Transitive dependency resolution:** Skill A depends on Skill B (via frontmatter). Verify both are fetched, both appear in `deps`, and harness skills list includes both resolved cache paths.
- **Two-level transitive resolution:** Skill A depends on Skill B, Skill B depends on Skill C. Verify all three are fetched.
- **Diamond dependency (no false positive):** Skill A depends on Skill B and Skill C; both B and C depend on Skill D. Verify D is fetched exactly once, no cycle error, and D's cache path appears only once in `h.Skills`.
- **Cycle detection:** Skill A depends on Skill B, Skill B depends on Skill A. Verify error contains "circular dependency".
- **Self-referencing skill:** Skill A depends on itself. Verify error contains "circular dependency".
- **Depth limit exceeded:** Chain of dependencies deeper than `MaxDepth`. Verify error contains "exceeded maximum dependency depth".
- **Breadth limit exceeded:** Skill with more than `MaxResources` dependencies. Verify error contains "exceeded maximum resource count".
- **No frontmatter (leaf node):** URL-fetched skill with plain Markdown (no `---`). Verify it resolves as a leaf node with no transitive fetches (same as Phase 1).
- **Empty dependencies list:** Skill with `dependencies: []` in frontmatter. Verify no transitive fetches.
- **Transitive dependency not in allowlist:** Skill A depends on Skill B at a URL outside `allowed_remote_resources`. Verify error contains "not in allowed_remote_resources".
- **Transitive dependency hash mismatch:** Skill A depends on Skill B; Skill B's content doesn't match its declared hash. Verify error contains "integrity check failed".
- **Mixed local and transitive:** Harness with local skills and one URL skill that has transitive deps. Verify local skills are untouched, URL skill and its transitive deps are all resolved.
- **Relative URL in dependency:** Skill directory at `https://github.com/org/skills/tree/abc123/rust` declares dependency `../common/formatting#sha256=<tree-hash>...`. Verify resolved to `https://github.com/org/skills/tree/abc123/common/formatting` and fetched as a directory via forge API.

**Depends on:** PR 1 (imports `internal/skill`)

**After merge:** Resolver supports transitive dependencies. Not yet wired into CLI (the CLI still calls `ResolveHarness` the same way, but transitive resolution now happens automatically for any URL-fetched skill that declares `dependencies:` in its frontmatter).

---

## PR 3: CLI wiring — transitive deps in sandbox upload + relative URL integration

**Scope:** Wires transitive dependency resolution into the CLI run flow. Ensures transitively resolved skills are uploaded to the sandbox alongside directly referenced skills. Adds `--max-depth` and `--max-resources` flags.

### Changes to `internal/cli/run.go`

**Add CLI flags:**

```go
cmd.Flags().Int("max-depth", 10, "maximum dependency depth for transitive resolution")
cmd.Flags().Int("max-resources", 50, "maximum total remote resources per harness")
```

The flag defaults match `DefaultMaxDepth` and `DefaultMaxResources`. Setting `--max-depth 0` disables transitive resolution entirely (no recursion). `ResolveOpts` no longer uses `0` as a sentinel — the CLI always passes the explicit flag value.

**Modify `runAgent()`:**

Between `h.ResolveRelativeTo(absFullsendDir)` and `h.ValidateFilesExist()`, the existing code currently calls `resolve.ResolveHarness`. Update the `ResolveOpts` to pass through the new limits:

```go
deps, err := resolve.ResolveHarness(ctx, h, resolve.ResolveOpts{
    WorkspaceRoot: workspaceRoot,
    FetchPolicy:   fetchPolicy,
    TraceID:       traceID,
    AuditLogPath:  auditLogPath,
    MaxDepth:      maxDepth,      // from --max-depth flag
    MaxResources:  maxResources,  // from --max-resources flag
})
```

**Modify skill upload loop:**

After `ResolveHarness` returns, `deps` now contains both direct and transitive dependencies. The harness `h.Skills` list already contains resolved local paths for direct skills (both local and URL-resolved). However, transitive skill dependencies are recorded in `deps` but not in `h.Skills`.

Two approaches (recommend Option A):

**Option A: ResolveHarness appends transitive skills to `h.Skills`.**
The resolver already modifies `h` in place (replacing URL fields with cache paths). Extend this: when a URL-fetched skill declares transitive skill dependencies, append the resolved cache paths to `h.Skills`. This way, the existing skill upload loop automatically handles transitive skills with no changes to `run.go`'s upload logic.

In `resolveTransitiveDeps`, after resolving each skill dependency:
```go
// Append transitively resolved skills to the harness Skills list
// so the existing upload loop picks them up. The resolved check in
// resolveState prevents diamond dependencies from appending duplicates.
h.Skills = append(h.Skills, localPath)
```

The `state.resolved` map ensures diamond dependencies (A→B→D, A→C→D) do not append D's cache path twice: `resolveURL` returns early for already-resolved URLs before reaching the append.

**Option B: Separate transitive deps in the upload loop.**
Iterate `deps` separately and upload any transitive skill deps not already in `h.Skills`. This keeps the resolver from modifying `h.Skills` beyond what the user declared, but requires more changes to `run.go`.

**Recommendation:** Option A. The resolver already modifies `h` in place (that's its documented contract). Appending transitive skills is consistent with this pattern and keeps `run.go` simple.

### Changes to `internal/resolve/resolve.go`

Pass the harness `h` through to `resolveTransitiveDeps` so it can append to `h.Skills`. This means `resolveTransitiveDeps` now takes `*harness.Harness` as a parameter (it was already available via the call chain, just needs to be threaded through).

### Security scanning of transitive dependencies

Transitive dependencies pass through the same security pipeline as direct dependencies:
- Fetched content is integrity-checked (hash verification) before caching.
- The existing `scanRepoContextFiles` function in `run.go` walks the sandbox's skill directories and scans all `SKILL.md` files found. Since transitive skills are uploaded to the sandbox as skill directories, they are automatically scanned.
- No additional scanning code is needed — the existing infrastructure handles it.

### Test updates

**Modify `internal/cli/run_test.go`:**
- `--max-depth` flag registration test.
- `--max-resources` flag registration test.

**Depends on:** PR 2

**After merge:** `fullsend run` supports transitive dependency resolution end-to-end. Example working harness:

```yaml
agent: agents/code.md
policy: policies/code.yaml
skills:
  - skills/local-skill
  - https://github.com/fullsend-ai/library/tree/8cd3799.../skills/rust-conventions#sha256=<tree-hash>...
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
```

Where the `rust-conventions` skill directory's `SKILL.md` contains:

```yaml
---
name: rust-conventions
dependencies:
  - ../cargo-integration#sha256=<tree-hash>...
  - https://github.com/fullsend-ai/library/tree/8cd3799.../skills/common/formatting#sha256=<tree-hash>...
---
# Rust Conventions skill content...
```

The resolver will:
1. Fetch `rust-conventions` skill directory via forge API (list files, fetch each), verify tree hash, cache under `tree/`.
2. Read `SKILL.md` from the cached `tree/` subdirectory, parse its frontmatter, discover 2 transitive skill dependencies.
3. Resolve `../cargo-integration` relative to the parent URL (sibling directory).
4. Fetch and cache both transitive skill directories (each via forge API with tree hash verification and allowlist checks).
5. Append all resolved cache `tree/` paths to `h.Skills`.
6. The sandbox upload loop uploads all skill directory trees.

---

## Security Considerations

### Cycle detection

The resolver uses two-phase DFS tracking to distinguish true cycles from valid DAG diamonds. The `inProgress` set tracks URLs currently on the DFS call stack; `resolved` tracks URLs that have been fully processed.

- **True cycle (A→B→A):** When resolving B's dependencies, A is found in `inProgress` → error: "circular dependency detected".
- **Diamond (A→B→D, A→C→D):** When resolving C's dependencies, D is found in `resolved` (not `inProgress`) → D's cached result is returned immediately, no re-fetch, no error, no budget consumed.

**Edge case:** Two different URLs serving identical content share a cache entry (content-addressed), but cycle detection operates on URLs, not content hashes. `A.md -> B.md -> A.md` is a cycle. `A.md -> B.md` where B has the same content as A is not a cycle.

### Depth limit

Maximum recursion depth defaults to 10 (configurable via `--max-depth`). This bounds the worst-case resolution time and prevents pathologically deep dependency chains. The depth counter increments at each recursive level. A skill at depth 10 that declares dependencies triggers an error before any of those dependencies are fetched.

### Breadth limit

Maximum total resources defaults to 50 (configurable via `--max-resources`). This counts all remote resources fetched per `ResolveHarness` call (direct + transitive). It prevents dependency explosion attacks where a skill declares 100 dependencies, each of which declares 100 more.

### Relative URL path traversal

When a skill directory at `https://github.com/org/skills/tree/abc123/rust` declares a dependency `../../../../attacker-org/evil`, RFC 3986 resolution produces `https://github.com/attacker-org/evil`. This URL passes the domain allowlist check (same domain), but **fails** the `allowed_remote_resources` prefix check:

```yaml
allowed_remote_resources:
  - https://github.com/org/skills/
```

The normalized URL `https://github.com/attacker-org/evil` does not match prefix `https://github.com/org/skills/`. The fetch is rejected.

**Critical:** The prefix check in `MatchingAllowedPrefix` operates on the **normalized** URL (after RFC 3986 `..` resolution), not the raw relative reference. This is already implemented in Phase 1 and applies to transitive dependencies without modification.

### Transitive dependency allowlist enforcement

Transitive dependencies must satisfy the same `allowed_remote_resources` constraints as direct dependencies. There is no separate allowlist for transitive deps — the harness-level allowlist (which itself must be a subset of the org-level allowlist) governs all fetches, direct or transitive. This is enforced in `resolveURL`, which checks `h.MatchingAllowedPrefix` for every URL regardless of depth.

### Integrity hash requirement

Transitive dependency references in SKILL.md frontmatter must include `#sha256=...` integrity hashes, just like direct references. For skill directory dependencies, the hash is a tree hash covering the entire directory tree. The existing `ParseIntegrityHash` validation in `resolveURL` enforces this uniformly. A dependency reference without a hash is rejected with a clear error message.

### Aggregate fetch latency

With `MaxResources=50` and the existing 30-second per-fetch timeout, the worst-case wall-clock time for a cold resolution is significant (50 resources, each potentially involving multiple forge API calls for directory listing and file fetching). In practice, most fetches complete in under a second, skill directories are small, and dependency graphs are shallow, so this is unlikely. A total wall-clock timeout for the entire `ResolveHarness` call is a reasonable future addition but is not included in Phase 2 — the existing per-fetch timeout and breadth limit provide sufficient protection for now.

---

## Files Summary

| File | PR | Action | Description |
|------|----|--------|-------------|
| `internal/skill/skill.go` | 1 | **Create** | SKILL.md frontmatter parser |
| `internal/skill/skill_test.go` | 1 | **Create** | Parser tests |
| `internal/resolve/resolve.go` | 2 | **Modify** | Add `resolveState`, recursive resolution, cycle detection, depth/breadth limits |
| `internal/resolve/relurl.go` | 2 | **Create** | `ResolveRelativeURL` function (RFC 3986) |
| `internal/resolve/relurl_test.go` | 2 | **Create** | Relative URL resolution tests |
| `internal/resolve/resolve_test.go` | 2 | **Modify** | Add transitive resolution, cycle, depth/breadth tests |
| `internal/cli/run.go` | 3 | **Modify** | Pass `MaxDepth`/`MaxResources` to resolver, add CLI flags |
| `internal/cli/run_test.go` | 3 | **Modify** | Flag registration tests |

---

## Verification

After PR 3 merges, verify Phase 2 end-to-end:

1. **Unit tests:** `make go-test` — all new and existing tests pass.
2. **Lint:** `make lint` passes.
3. **Local-only harness (regression):** Run an existing harness with only local paths — no behavioral change from Phase 1.
4. **Single-level URL harness (regression):** Run a harness with URL-referenced skills that have no `dependencies:` frontmatter — same behavior as Phase 1.
5. **Transitive dependency resolution:** Create a test harness referencing a URL-hosted skill directory whose SKILL.md frontmatter declares `dependencies:` with another URL-hosted skill directory. Verify both skill directories are fetched via forge API, cached as directory trees, and uploaded to the sandbox.
6. **Relative URL resolution:** Create a skill directory that references a dependency via a relative path (`../common/formatting#sha256=<tree-hash>...`). Verify the relative reference is resolved to a sibling directory against the parent URL and fetched correctly via forge API.
7. **Cycle detection:** Create two skills that reference each other in their `dependencies:`. Verify the resolver fails with a "circular dependency" error.
8. **Depth limit:** Create a chain of skills deeper than 10 levels. Verify the resolver fails with a "exceeded maximum dependency depth" error. Verify `--max-depth 3` lowers the limit.
9. **Breadth limit:** Create a skill that declares more than 50 transitive dependencies. Verify the resolver fails with a "exceeded maximum resource count" error. Verify `--max-resources 5` lowers the limit.
10. **Transitive dep not in allowlist:** Create a skill whose transitive dependency URL is outside `allowed_remote_resources`. Verify rejection with "not in allowed_remote_resources" error.
11. **Transitive dep hash mismatch:** Create a skill whose transitive dependency has a wrong hash. Verify rejection with "integrity check failed" error.
12. **Audit log:** Verify that transitive dependency fetches produce audit log entries with `fetch_type: "static"` and correct `allowed_by` values.
13. **Offline mode with transitive deps:** Pre-populate cache for all transitive dependencies, then run `fullsend run --offline`. Verify cache hits succeed. Remove one transitive dep from cache and verify failure.

---

## Future Phases

### Phase 3: Lock files (2 PRs) — COMPLETE
See `docs/plans/universal-harness-access-phase3.md` for detailed plan.

### Phase 4: Runtime dependency loading (3 PRs)
See `docs/plans/universal-harness-access-phase4.md` for detailed plan.
