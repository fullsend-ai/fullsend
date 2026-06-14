# Implementation Plan: ADR-0045 Forge-Portable Harness Schema — Phase 1

## Context

ADR-0045 makes harness YAML files self-contained and forge-portable. Today, agent identity (`role`, `slug`) lives in `config.yaml`'s `agents:` block, separate from the harness — adding a new agent requires editing three files. Forge-specific fields (pre/post scripts, skills, runner_env) sit at the harness top level, making harnesses implicitly GitHub-only.

This plan adds `role`, `slug`, `base`, and `forge:` to the harness schema. Phase 1 is detailed below (backward-compatible additions). Phases 2-4 (adopt, deprecate, remove the `agents:` block) follow as high-level outlines.

ADR: `docs/ADRs/0045-forge-portable-harness-schema.md`

### Relationship to ADR-0038 (Universal Harness Access)

ADR-0038 makes harness *resources* (agents, skills, policies) URL-addressable. ADR-0045 makes *the harness itself* portable via `base` composition and `forge:` sections. The two complement each other:

- **Shared infrastructure:** `base` URL resolution reuses the same `internal/fetch` (SSRF-hardened fetcher, content-addressed cache, audit logging) and `internal/resolve` (integrity hashing, allowlist checking) packages that ADR-0038 built. `base` is treated as a declarative resource — the same rules apply (HTTPS-only, mandatory `#sha256=...`, `allowed_remote_resources` prefix check).
- **Lock file integration:** ADR-0038 Phase 3 (`internal/lock/`, `fullsend lock` CLI — shipped, PR #2082) pins resolved URLs in `lock.yaml`. The `base` field is a URL-referenced resource and must participate in the lock file. PR 4 adds `base` as a new `DependencyEntry.Field` value (`"base"`) so `fullsend lock` records it alongside agent/skill/policy deps.
- **Pipeline ordering:** `base` resolution happens *before* `resolve.ResolveHarness` — it produces the merged harness whose agent/skill/policy URLs are then resolved by the existing Phase 1/2 pipeline. The two are separate stages, not interleaved.

**ADR-0038 implementation status** (as of this plan):
| Phase | Status | Key artifacts |
|-------|--------|---------------|
| Phase 1 (MVP) | Shipped | `internal/fetch/`, `internal/harness/url.go`, `internal/resolve/`, CLI `--offline` |
| Phase 2 (Transitive deps) | Shipped | `internal/skill/`, recursive resolver, `--max-depth`/`--max-resources` |
| Phase 3 (Lock files) | Shipped | `internal/lock/`, `fullsend lock` CLI (PR #2082) |
| Phase 4 (Runtime fetch) | Not started | — |

## PR Dependency Graph

```
PR 1 (ForgeConfig + merge) ──> PR 3 (ResolveForge + loadRaw) ──┬──> PR 5 (full pipeline integration)
                                                                │      ↑
PR 2 (role/slug fields) ───────────────────────────────────────>├──> PR 4 (base composition) ──┘
        │                                                       │
        └──> PR 6 (scaffold templates: add role/slug)           │
                                                                │
PR 7 (nil-vs-empty YAML tests)  [no dependencies, parallel with all]
```

PRs 1, 2, 7 can start in parallel. PR 4 depends on PRs 1, 2, and 3 (`loadRaw` for loading base harnesses without consuming forge maps). ADR-0038 Phase 3 (lock CLI, PR #2082) has already merged.

---

## PR 1: ForgeConfig struct and merge logic

**Scope:** New struct, merge function, validation. No callers in the load pipeline — pure library code.

**Create `internal/harness/forge.go`:**
- `ForgeConfig` struct with `PreScript`, `PostScript`, `Skills`, `ValidationLoop`, `RunnerEnv`
- `validForgeKeys = map[string]bool{"github": true, "gitlab": true}`
- `(h *Harness) ResolveForge(platform string) error` — merges forge overrides into harness in place per ADR rules:
  - Scalars: forge overrides if non-empty
  - Skills: top-level + forge (concatenated)
  - RunnerEnv: top-level + forge map, forge wins on key conflict
  - ValidationLoop: forge replaces entirely if non-nil
  - Sets `h.Forge = nil` after merge (consumed)
- Unexported `mergeForgeConfig(h *Harness, fc *ForgeConfig)` for the merge logic
- `ResolveForge` always runs before `Validate()`, so forge-overridden `PreScript`/`PostScript`/`ValidationLoop.Script` paths are validated by `ValidateResourceTypes()` (which rejects URLs in executable fields). No gap exists — `Validate()` sees the post-merge state. This holds whether `ResolveForge` runs inside `Load()` (standalone) or after base chain merging (composition).

**Modify `internal/harness/harness.go`:**
- Add `Forge map[string]*ForgeConfig` with `yaml:"forge,omitempty"` to `Harness` struct
- Add `validateForge()` — reject unrecognized keys (with "valid keys are: github, gitlab" error message), validate each ForgeConfig's fields
- Call `validateForge()` from `Validate()`

**Create `internal/harness/forge_test.go`:**
- Scalar override, skills concat, runner_env merge, validation_loop replace
- No forge section (no-op), unknown platform error, unrecognized key rejection
- Nil skills in forge inherits top-level; empty `skills: []` adds nothing
- `h.Forge` is nil after merge

**After merge:** ForgeConfig and merge logic exist with tests. No runtime callers. Existing harnesses unchanged.

---

## PR 2: Role and slug fields

**Scope:** Add optional fields to Harness struct with validation.

**Modify `internal/harness/harness.go`:**
- Add `Role string` with `yaml:"role,omitempty"` and `Slug string` with `yaml:"slug,omitempty"` to `Harness` struct
- Add `validRole = regexp.MustCompile('^[a-z][a-z0-9_-]*$')` — consistent with `mintcore.RolePattern` (line 23 of `internal/mintcore/patterns.go`) but NOT imported (avoids coupling harness→mintcore)
- Add `validSlugName = regexp.MustCompile('^[a-zA-Z0-9][a-zA-Z0-9_-]*$')`
- In `Validate()`: if role/slug are set, validate patterns. Reject double-hyphens in role (`strings.Contains(role, "--")`) to match `mintcore.ValidateRoleName` behavior. Both remain **optional** in Phase 1.

**Tests:** Parse with/without role/slug (backward compat), invalid patterns rejected, valid values accepted.

**After merge:** Harness YAML accepts `role` and `slug`. Optional, validated when present.

---

## PR 3: Wire ResolveForge into the load pipeline

**Scope:** Adds `--forge` CLI flag and wires `ResolveForge` into the load pipeline. For standalone harnesses (no `base`), `ResolveForge` runs inside `Load()` between Unmarshal and Validate (per ADR-0045 §Implementation). For base composition, `ResolveForge` runs once on the final merged result (see PR 5).

**Modify `internal/harness/harness.go`:**
- Add `LoadOpts` struct with `ForgePlatform string`
- Add `LoadWithOpts(path string, opts LoadOpts) (*Harness, error)` — calls `yaml.Unmarshal`, then `h.ResolveForge(opts.ForgePlatform)`, then `h.Validate()`. This ordering is required because `Validate()` would reject sentinel zero-value structs (e.g., empty `validation_loop`) that `ResolveForge` needs to process first.
- Add unexported `loadRaw(path string) (*Harness, error)` — unmarshal only, no `Validate()`, no `ResolveForge`. Used by `LoadWithBase` (PR 4) to load base harnesses without consuming their forge maps before merging.
- Original `Load()` unchanged (calls Unmarshal → Validate, no forge). `LoadWithOpts` is the forge-aware entry point.

**Modify `internal/cli/run.go`:**
- Add `--forge` string flag (optional)
- Add `detectForgePlatform(flag string) string`:
  - Flag set → return it (validate against `validForgeKeys`)
  - `GITHUB_ACTIONS=true` → `"github"`
  - `GITLAB_CI=true` → `"gitlab"`
  - Otherwise → `""` (skip)
- Pass detected platform into `LoadWithOpts` so forge resolution happens inside `Load()` for standalone harnesses.

**Modify `internal/cli/lock.go`:**
- Add `--forge` flag to `fullsend lock` so it can resolve forge-specific overrides before locking.
- **Multi-forge locking behavior:** When `--forge` is specified, `fullsend lock` resolves that platform's forge overrides and locks the resulting URL set. When `--forge` is omitted but the harness has a `forge:` section, `fullsend lock` iterates all forge keys (e.g., `github`, `gitlab`): for each key, it calls `loadRaw` → `ResolveForge(key)` on a copy of the unmarshaled harness to produce that variant's URL set, then resolves and records all URLs. The union of dependencies across all variants is written to the lock file. Duplicate URLs (same URL in multiple variants) are deduplicated by URL. If one variant fails (e.g., a forge-specific URL is unreachable), the entire `fullsend lock` invocation fails — partial lock files are not written.

**After merge:** `fullsend run --forge github triage` applies forge-specific overrides. Auto-detection works in CI. Without `forge:` section, behavior is identical to today.

---

## PR 4: Base composition

**Scope:** Harness inheritance via `base` field. Reuses the existing ADR-0038 resolution infrastructure — does NOT call `fetch.FetchURL` directly.

### Integration with ADR-0038

`base` is a URL-referenced declarative resource, just like `agent` or `policy`. It uses the same fetch → verify hash → cache → audit pipeline that `internal/resolve/resolveURL` already implements. However, `base` resolution happens at a different stage (before the merged harness's own URLs are resolved), so it cannot go through `resolve.ResolveHarness` directly.

The approach: extract the core fetch-verify-cache logic from `resolve.resolveURL` into a reusable helper (or call `resolveURL` directly with a synthetic field name `"base"`), so `base` resolution gets SSRF protection, integrity checking, content-addressed caching, audit logging, and allowlist enforcement for free.

For **lock file integration**, `base` URLs are recorded as `DependencyEntry` entries with `Field: "base"`. The `fullsend lock` CLI (shipped, PR #2082) resolves `base` alongside other resources and pins them in `lock.yaml`. At run time, if a lock entry exists for the `base` URL and the hash matches, the resolver skips re-fetching.

**Modify `internal/harness/harness.go`:**
- Add `Base string` with `yaml:"base,omitempty"` to `Harness`
- `ValidateResourceTypes()`: `base` URLs require `#sha256=...` hash (same rule as agent/policy)

**Create `internal/harness/compose.go`:**
- `MaxBaseDepth = 5`
- `ComposeOpts` struct:
  - `WorkspaceRoot string` — for cache paths and relative path resolution
  - `FetchPolicy fetch.FetchPolicy` — passed to resolver (SSRF protection, offline mode)
  - `AuditLogPath string` — for fetch audit entries
  - `OrgAllowlist []string` — for URL prefix validation
  - `HarnessAllowlist []string` — the child harness's `AllowedRemoteResources` (base URLs must pass prefix check)
  - `ForgePlatform string` — passed to `ResolveForge` after base chain merge (empty = skip)
- `LoadWithBase(path string, opts ComposeOpts) (*Harness, []resolve.Dependency, error)`:
  - Returns the merged harness plus a list of `resolve.Dependency` (with `Field: "base"`) for any URL bases fetched. The CLI layer converts these to `lock.DependencyEntry` at the call site, following the same pattern as the existing `runLock` code (`internal/cli/lock.go:151-159`). This keeps `internal/harness` from importing `internal/lock`. When no base is present, the dependency list is nil.
  - Loads the leaf harness via `loadRaw(path)` (unmarshal only — no `Validate()`, no `ResolveForge`). This preserves the forge map for merging.
  - If `base` is absent, calls `ResolveForge(opts.ForgePlatform)` then `Validate()` and returns — equivalent to `LoadWithOpts`.
  - If `base` is a local path: `loadRaw` from disk, merge
  - If `base` is a URL: resolve via the same fetch/cache/audit pipeline as `resolve.resolveURL` — check `AllowedRemoteResources` prefix, check/fetch from content-addressed cache (`fetch.CacheGet`/`CachePut`), verify `#sha256=...` integrity hash, write `fetch.AppendFetchAudit` entry. Then `loadRaw` from the cache path and merge.
  - Recursively load base chain (cycle detection via canonical path/URL set, depth ≤ `MaxBaseDepth`). Each harness in the chain is loaded via `loadRaw` to preserve forge maps.
  - After the full base chain is merged, calls `ResolveForge(opts.ForgePlatform)` once on the final merged result, then calls `Validate()`. This matches the ADR's resolution order: `base harness (recursive) → child overrides → ResolveForge(platform)`.
  - `mergeHarness(base, child *Harness)` — same inheritance rules as forge merge:
    - Scalars: child overrides base if non-zero
    - Skills, Plugins, Providers, APIServers: concatenated (base + child)
    - RunnerEnv: base map merged with child map, child keys win
    - ValidationLoop, Security: child replaces if non-nil
    - HostFiles: concatenated (base + child order), last-writer-wins dedup by `Dest` (exact string comparison, no path canonicalization) — child entries override base entries with the same `Dest`
    - Forge: key-by-key merge; per-platform ForgeConfig uses same merge rules
    - `base` field consumed (empty on merged result)
  - **Design decision:** Base harnesses must be complete, valid harnesses (not partial fragments). `loadRaw` still unmarshals the full `Harness` struct; `Validate()` runs on the final merged result so incomplete base fields (e.g., missing `agent`) are only rejected if the merged harness itself is incomplete. The ADR's open question about fragment support (lines 703–707) is deferred — if needed later, `loadRaw` can be relaxed without changing the composition API.
- When `base` is absent, `LoadWithBase` behaves identically to `LoadWithOpts` (deps is nil, harness unchanged)

**Modify `internal/resolve/resolve.go`:**
- Export a `FetchAndVerify(ctx, rawURL string, allowedPrefixes []string, opts ResolveOpts) (url, sha256, localPath string, content []byte, err error)` function that `compose.go` can call for URL-referenced bases. This avoids duplicating the fetch → hash verify → cache → audit logic. Returns the clean URL, verified hash, cache path, and content bytes. The caller (`LoadWithBase`) constructs `lock.DependencyEntry` from these values.
- Alternative: keep `resolveURL` unexported but add a `ResolveBase(ctx, rawURL string, allowedPrefixes []string, opts ResolveOpts) (url, sha256, localPath string, content []byte, err error)` wrapper.

**Modify `internal/lock/lock.go`:**
- No schema changes needed — `DependencyEntry` already has a `Field` string. `base` URLs are recorded with `Field: "base"`.

**Modify `internal/cli/lock.go`:**
- `runLock()` must call `LoadWithBase` before `resolve.ResolveHarness` so the base chain is resolved first, and base dependencies are included in the lock file alongside agent/skill/policy deps.
- Add `case m.field == "base":` to `resolveFromLock`'s mutation switch (line 237). This is a no-op — base composition is already resolved before lock-based resolution runs, so `Field: "base"` entries only need cache verification, not harness mutation. Without this case, the `default` branch would incorrectly append the base's cache path to `h.Skills`.

**Create `internal/harness/compose_test.go`:**
- Local base: child overrides base scalars
- URL base (httptest): verify fetch, hash check, cache, audit entry
- Chained bases: A → B → C, correct merge order
- Cycle detection: A → B → A, error
- Depth exceeded: chain > 5, error
- All merge behaviors: scalar override, skills concat, runner_env merge, host_files dedup, forge merge
- No base = same as Load, base field consumed
- URL base not in allowlist → rejected
- URL base hash mismatch → rejected
- URL base cached (second load hits cache, no fetch)

**Depends on:** PR 1 (ForgeConfig merge), PR 2 (role/slug in merged struct), PR 3 (`loadRaw` and `LoadOpts`). `LoadWithBase` uses `loadRaw` from PR 3 to load each harness in the chain without consuming forge maps, then calls `ResolveForge` once on the merged result. ADR-0038 Phase 3 (lock CLI, PR #2082) has already merged, so lock file integration is included from the start.

**After merge:** Harnesses can reference a base via local path or URL. URL bases go through the same fetch/cache/audit/allowlist pipeline as all other URL resources. Without `base`, identical to today.

---

## PR 5: Full pipeline integration

**Scope:** Wire everything together in the correct order. Ensure the pipeline is consistent across `fullsend run`, `fullsend lock`, and future CLI commands.

**Modify `internal/cli/run.go`:**
- Replace `harness.LoadWithOpts()` with `harness.LoadWithBase()`, passing `ForgePlatform` through `ComposeOpts`
- Capture `baseDeps` (`[]resolve.Dependency`) from `LoadWithBase`'s second return value; convert to `[]lock.DependencyEntry` using the same pattern as the existing `runLock` conversion (`internal/cli/lock.go:151-159`), then append to the lock file's dependency list alongside agent/skill/policy deps
- Final pipeline ordering in `runAgent()`:
  ```
  1. detectForgePlatform(flag)            — determine platform from flag or CI env vars
  2. LoadWithBase(path, composeOpts)       — loadRaw each harness in base chain, merge,
                                             ResolveForge once on merged result, Validate
  3. h.ResolveRelativeTo(absFullsendDir)   — resolve relative paths to absolute
  4. load lock.yaml, check staleness       — ADR-0038 Phase 3 lock-aware check
  5. resolve.ResolveHarness(ctx, h, opts)  — resolve URL-referenced agent/skill/policy to cache paths
  6. h.ValidateRunnerEnv()                 — check ${VAR} refs are defined
  7. h.ValidateFilesExist()                — check all resolved files exist on disk
  ```
  Steps 4-5 are the existing ADR-0038 flow. Step 2 is the new ADR-0045 entry point. Steps 3, 6, 7 are existing. The key insight: `LoadWithBase` produces a fully-merged, forge-resolved, validated harness with resolved `base` but still-unresolved agent/skill/policy URLs. Then `resolve.ResolveHarness` handles those as before. Per the ADR, `ResolveForge` runs once on the final merged harness (not per-harness in the chain), so forge maps from base harnesses are properly merged before consumption.
- For standalone harnesses (no `base`), `LoadWithBase` degrades to `LoadWithOpts` — same behavior as PR 3.
- Log `Role` and `Slug` if present in the run output: `printer.KeyValue("Role", h.Role)`

**Modify `internal/cli/lock.go`:**
- Same pipeline ordering for `fullsend lock`: `LoadWithBase` (merge base chain, `ResolveForge` once on merged result) → `ResolveRelativeTo` → `resolve.ResolveHarness`. The lock file records dependencies from both the base chain and the resource resolution.
- Fix `HasURLReferences()` short-circuit: move the check after `LoadWithBase` and change the condition to `!h.HasURLReferences() && len(baseDeps) == 0`. Without this, a harness whose only remote references are in `base` (all agent/policy/skills are local paths) would skip lock file generation, losing the base dependency entries. `HasURLReferences()` only checks Agent/Policy/Skills — it has no knowledge of `base` (which is already consumed by `LoadWithBase`).

**Create `internal/harness/integration_test.go`:**
- End-to-end: YAML with base + forge + role/slug → LoadWithBase → verify correct merged state (forge maps merged across base chain before consumption)
- Base with URL agent + forge override on skills → verify full pipeline produces expected Skills list
- Backward compat: existing harness (no base, no forge, no role/slug) → identical to current behavior

**After merge:** Phase 1 is complete. Full backward compatibility maintained.

---

## PR 6: Scaffold templates — add role and slug

**Scope:** Update embedded harness templates.

**Modify each file in `internal/scaffold/fullsend-repo/harness/`:**
- `triage.yaml`: `role: triage`, `slug: fullsend-ai-triage`
- `code.yaml`: `role: coder`, `slug: fullsend-ai-coder`
- `review.yaml`: `role: review`, `slug: fullsend-ai-review`
- `fix.yaml`: `role: coder`, `slug: fullsend-ai-coder` (reuses coder app)
- `retro.yaml`: `role: retro`, `slug: fullsend-ai-retro`
- `prioritize.yaml`: `role: prioritize`, `slug: fullsend-ai-prioritize`

Existing `TestHarnessesLoadAndValidate` (`internal/scaffold/scaffold_test.go:614`) validates these automatically.

**Depends on:** PR 2

---

## PR 7: Nil vs empty YAML unmarshaling tests

**Scope:** Lock in `gopkg.in/yaml.v3` behavior for inheritance semantics. Independent of all other PRs.

**Create `internal/harness/yaml_semantics_test.go`:**
- Test raw `yaml.Unmarshal` (no `Validate()`) for each field type:
  - `skills`: absent → nil slice; `skills: []` → non-nil empty slice; `skills: [a]` → populated
  - `runner_env`: absent → nil map; `runner_env: {}` → non-nil empty map
  - `validation_loop`: absent → nil pointer; `validation_loop: {}` → non-nil zero struct
  - `forge`: absent → nil map; `forge: {}` → non-nil empty map; nested ForgeConfig fields

---

## Future Phases

### Phase 2: Adopt (3-4 PRs)
- Update `fullsend install` to write role/slug into harness files (in addition to config.yaml agents block)
- Generate thin `base:` wrappers pointing to upstream scaffold harnesses by URL (with `#sha256=...` from the release tag, locked via `fullsend lock`)
- Move GitHub-specific fields into `forge.github:` blocks in scaffold templates
- Add `harness.DiscoverAgents(dir)` — scan `harness/*.yaml` for role/slug inventory

### Phase 3: Deprecate (2-3 PRs)
- Add `Lint()` method for non-fatal diagnostics (warn when `role` missing) — separate from `Validate()` to avoid breaking callers
- Make `OrgConfig.Agents` use `yaml:"agents,omitempty"`, fall back to harness discovery when absent
- Update `loadKnownSlugs()` and `SecretsLayer` to check harness files first

### Phase 4: Remove (2 PRs)
- Require `role` in `Validate()`
- Remove `Agents []AgentEntry` from `OrgConfig`, remove `AgentSlugs()`, update all consumers. Consider config version bump to "2".

---

## Verification

After PR 5 merges, verify Phase 1 end-to-end:

1. `make go-test` — all new and existing tests pass
2. `make go-vet` — no issues
3. `make lint` — passes
4. **Backward compat:** existing harness without role/slug/base/forge loads identically
5. **Forge merge:** harness with `forge.github:` block + `--forge github` → correct merged values
6. **Base composition (local):** harness with `base:` referencing a local base → correct inheritance
7. **Base composition (URL):** harness with `base: https://...#sha256=...` → fetched, cached, merged, audit logged
8. **Base + lock file:** `fullsend lock triage` records `base` URL in `lock.yaml`; subsequent `fullsend run` uses cache hit
9. **Role/slug:** parsed and logged in run output
10. **Scaffold:** `TestHarnessesLoadAndValidate` passes with new role/slug fields
