# Implementation Plan: ADR-0045 Forge-Portable Harness Schema — Phase 3 (Deprecate)

## Context

Phase 2 (shipped) completed the "Adopt" milestone: `fullsend install` generates thin wrapper harness files with `base:`, `role:`, and `slug:` in the `.fullsend` config repo. Scaffold templates use `forge.github:` blocks for platform-specific fields. `harness.DiscoverAgents()` scans local harness directories for agent identity. `fullsend lock --all` locks all harnesses in a single pass. Both the `config.yaml` `agents:` block and harness wrapper files now contain role/slug (dual-write).

Phase 3 completes the "Deprecate" milestone from the ADR migration path. Specifically:

1. **`Lint()` diagnostic method warns on missing `role`** — today `Validate()` returns hard errors only. Phase 3 adds a separate `Lint()` method that returns non-fatal diagnostics (warnings), starting with "role is not set; it will be required in a future version." This keeps `Validate()` callers (which treat all errors as hard stops) unaffected.

2. **Consumers migrate to harness-first discovery** — today `loadKnownSlugs()`, `runUninstall`, and `runGitHubUninstall` read agent identity exclusively from `config.yaml`'s `agents:` block. Phase 3 adds remote harness discovery via `forge.Client.ListDirectoryContents` + `GetFileContentAtRef` (used for harness wrapper discovery in config repos, distinct from skill directory fetching which uses `gitfetch.FetchTree`), and migrates these consumers to check harness files first, falling back to the `agents:` block.

3. **`OrgConfig.Agents` becomes optional** — the `Agents` field gains `omitempty` so config.yaml can omit the `agents:` block. When present during load, a deprecation notice is logged. The dual-write during install continues (Phase 4 stops it).

ADR: `docs/ADRs/0045-forge-portable-harness-schema.md`
Phase 1 plan: `docs/plans/adr-0045-forge-portable-harness-phase1.md`
Phase 2 plan: `docs/plans/adr-0045-forge-portable-harness-phase2.md`

### Relationship to Phase 2

Phase 3 builds on Phase 2's deliverables:

| Phase 2 artifact | Phase 3 usage |
|---|---|
| `Harness.Role`, `Harness.Slug` fields | `Lint()` warns when `role` is absent |
| `DiscoverAgents()` + `LoadRaw()` | Foundation for remote harness discovery (same parse logic, different I/O) |
| Wrapper harness files in config repo | Remote discovery reads these instead of `config.yaml` `agents:` block |
| `forge.github:` blocks in scaffold templates | Lint can validate forge section completeness in future phases |
| ~~`HarnessWrappersLayer` dual-write~~ | ~~Ensures both sources exist during Phase 3 transition; Phase 4 removes the `agents:` write~~ *(layer removed — PR #5425)* |

### Key design insight: remote vs local discovery

All current consumers of `OrgConfig.Agents` operate on **remote config repo data** (fetched via `forge.Client`) during install/uninstall CLI commands. `harness.DiscoverAgents()` operates on **local harness files on disk**. These are fundamentally different data sources:

- **Local discovery** (`DiscoverAgents`): used at agent runtime — the runner reads harness files from the cloned `.fullsend/` directory. No migration needed here; the runner already loads harness files directly.
- **Remote discovery** (new): used during install/uninstall CLI commands — the CLI reads the `.fullsend` config repo via the forge API. Phase 2 writes wrapper harness files there, so remote discovery can now read them instead of the `agents:` block.

All three remote consumers (`loadKnownSlugs`, `runUninstall`, `runGitHubUninstall`) already have fallback paths that derive slugs from `DefaultAgentRoles()` + naming convention, making the migration lower-risk.

### What Phase 3 does NOT do

- Does NOT require `role` in `Validate()` (Phase 4)
- Does NOT remove `AgentSlugs()` or the `Agents` field from `OrgConfig` (Phase 4)
- Does NOT stop the dual-write in install (Phase 4)
- Does NOT remove the fallback to `agents:` block (Phase 4)

## PR Dependency Graph

```
PR 1 (Lint diagnostic infra) ──> PR 3 (wire Lint into CLI)
                                                           \
PR 2 (remote harness discovery) ──> PR 4 (migrate loadKnownSlugs) ──> PR 6 (OrgConfig.Agents omitempty)
                                 \                                  /
                                  └──> PR 5 (migrate uninstall) ──┘
```

PRs 1 and 2 can start in parallel (no dependencies on each other or on Phase 2 PR 6). PR 3 depends on PR 1. PRs 4 and 5 depend on PR 2. PR 6 depends on PRs 4 and 5 (all consumers migrated before making the field optional).

---

## PR 1: Lint() diagnostic infrastructure and role warning

**Scope:** New diagnostic type, `Lint()` method on Harness, and a "missing role" warning. No callers — pure library code.

**Create `internal/harness/lint.go`:**

- `DiagnosticSeverity` type:
  ```go
  type DiagnosticSeverity int

  const (
      SeverityWarning DiagnosticSeverity = iota
      SeverityError
  )
  ```
- `Diagnostic` struct:
  ```go
  type Diagnostic struct {
      Severity DiagnosticSeverity
      Field    string // e.g. "role", "forge.github.pre_script"
      Message  string
  }
  ```
- `(d Diagnostic) String() string` — formats as `"warning: role: <message>"` or `"error: role: <message>"`
- `(h *Harness) Lint() []Diagnostic`:
  - If `h.Role == ""`: append warning `{SeverityWarning, "role", "role is not set; it will be required in a future version"}`
  - Returns nil when no diagnostics are found (not an empty slice — callers can do `if diags := h.Lint(); len(diags) > 0`)
  - Called AFTER `Validate()` / `LoadWithBase()` — operates on the post-merge, post-forge-resolution harness. `Lint()` assumes the harness is already valid; callers should not call `Lint()` if `Validate()` failed.
  - Unlike `Validate()`, `Lint()` never returns an error — it returns a slice of diagnostics that callers can print or ignore.

**Design note:** `Lint()` is intentionally separate from `Validate()` rather than adding a "warnings" return channel to `Validate()`. This avoids changing `Validate()`'s signature (`error` → `([]Diagnostic, error)`) which would require updating every caller. The two methods serve different purposes: `Validate()` gates execution (hard stop), `Lint()` provides advisory feedback.

**Future lint rules** (not in this PR, but the infrastructure supports them):
- `slug` is missing
- `forge:` section has only one platform (informational)
- `base:` uses a pinned commit SHA that differs from the running CLI version

**Create `internal/harness/lint_test.go`:**
- Harness with role → no diagnostics
- Harness without role → one warning diagnostic with field "role"
- Harness with role and slug → no diagnostics
- Diagnostic.String() formats correctly for warning and error severities
- `Lint()` returns nil (not empty slice) when no issues found

**After merge:** `Lint()` and `Diagnostic` exist as tested library code. No callers yet. `Validate()` is unchanged.

---

## PR 2: Remote harness agent discovery

**Scope:** Add a function that discovers agent identity (role, slug) from harness files in a remote config repo via the forge API. Analogous to `DiscoverAgents()` but reads via `forge.Client` instead of the local filesystem.

**Create `internal/harness/discover_remote.go`:**

- `DiscoverRemoteAgents(ctx context.Context, client forge.Client, owner, repo, ref string) ([]AgentInfo, error)`:
  - Calls `client.ListDirectoryContents(ctx, owner, repo, "harness", ref, false)` to list files in the `harness/` directory
  - Filters for `.yaml` and `.yml` extensions (same as `DiscoverAgents`)
  - For each YAML file: calls `client.GetFileContentAtRef(ctx, owner, repo, entry.Path, ref)` to read the file content
  - Unmarshals each file into a `Harness` struct using the same minimal parse as `LoadRaw` — but from bytes rather than a file path. Extract a helper: `ParseRaw(data []byte) (*Harness, error)` that does `yaml.Unmarshal` without file I/O, validation, or forge resolution. `LoadRaw` can be refactored to call `ParseRaw` internally.
  - Extracts `h.Role` and `h.Slug`; skips files where both are empty
  - Returns sorted by `Role` then `Filename` (same ordering as `DiscoverAgents`)
  - If `ListDirectoryContents` returns `forge.ErrNotFound` (no `harness/` directory), returns `(nil, nil)` — same convention as `DiscoverAgents` for non-existent directories
  - Per-file errors (parse failures, `GetFileContentAtRef` failures) are collected into a multi-error; valid files are still returned. Same partial-result semantics as `DiscoverAgents`.

**Refactor `internal/harness/harness.go`:**

- Extract `ParseRaw(data []byte) (*Harness, error)` from `LoadRaw`:
  ```go
  func ParseRaw(data []byte) (*Harness, error) {
      var h Harness
      if err := yaml.Unmarshal(data, &h); err != nil {
          return nil, err
      }
      return &h, nil
  }

  func LoadRaw(path string) (*Harness, error) {
      data, err := os.ReadFile(path)
      if err != nil {
          return nil, err
      }
      return ParseRaw(data)
  }
  ```
- `ParseRaw` is exported for use by `DiscoverRemoteAgents` and any other caller that has raw YAML bytes (e.g., test helpers). `LoadRaw` remains the convenience wrapper for file-based loading.

**Create `internal/harness/discover_remote_test.go`:**
- Mock forge client (implement `forge.Client` interface with in-memory file map)
- Directory with multiple harness files → returns sorted AgentInfo list
- No `harness/` directory (`ErrNotFound`) → `(nil, nil)`
- File without role/slug → skipped
- Malformed YAML → multi-error, other files still returned
- `GetFileContentAtRef` failure for one file → multi-error, other files returned
- Empty `harness/` directory → empty list, no error
- Results match what `DiscoverAgents` would return for the same content on disk

**After merge:** `DiscoverRemoteAgents` and `ParseRaw` exist as tested library functions. No production callers. The forge API surface required (`ListDirectoryContents`, `GetFileContentAtRef`) already exists.

---

## PR 3: Wire Lint() into fullsend run and lock

**Scope:** Call `Lint()` after harness loading in `fullsend run` and `fullsend lock`, printing warnings to stderr. Non-fatal — commands still succeed.

**Modify `internal/cli/run.go`:**

- After `LoadWithBase()` returns successfully, call `h.Lint()`
- For each diagnostic, print via `printer.Warning(diag.String())`
- No early exit — lint diagnostics are informational only
- Example output:
  ```
  ⚠ warning: role: role is not set; it will be required in a future version
  ```

**Modify `internal/cli/lock.go`:**

- Same pattern: call `h.Lint()` after `LoadWithBase()` in `runLock()`
- For `--all` mode: lint each harness after loading, print diagnostics with the harness filename as context: `printer.Warning(fmt.Sprintf("%s: %s", harnessName, diag.String()))`

**Check `internal/ui/printer.go`:**

- Verify `Warning(msg string)` method exists (or `Warn`). If not, add it — print to stderr with a `⚠` prefix, colored yellow if terminal supports it. Follow existing `printer.Error()` / `printer.Info()` patterns.

**Create/modify test files:**

- `internal/cli/run_test.go`: test that a harness without `role` produces a warning line in output but command succeeds
- `internal/cli/lock_test.go` (or `lock_all_test.go`): same for lock path

**After merge:** `fullsend run` and `fullsend lock` emit warnings for harnesses missing `role`. No behavioral change — commands succeed regardless.

**Depends on:** PR 1

---

## PR 4: Migrate loadKnownSlugs to harness-first discovery

**Scope:** Change `loadKnownSlugs()` in `internal/cli/admin.go` to prefer harness wrapper files over the `config.yaml` `agents:` block. Emits a deprecation notice when falling back to the `agents:` block.

**Modify `internal/cli/admin.go`:**

- Rename `loadKnownSlugs` → `loadKnownSlugsLegacy` (unexported, kept as fallback)
- New `loadKnownSlugs(ctx context.Context, client forge.Client, owner, configRepo, ref string, printer *ui.Printer) map[string]string`:
  1. Call `harness.DiscoverRemoteAgents(ctx, client, owner, configRepo, ref)`
  2. If result is non-empty: build `map[role]slug` from `[]AgentInfo`, return it
  3. If result is empty (no harness files or no role/slug in them): call `loadKnownSlugsLegacy` (reads `config.yaml` `agents:` block)
  4. If legacy returns non-empty: emit deprecation notice via `printer.Warning("agent identity read from config.yaml agents: block; migrate to harness files with role/slug fields")`
  5. If legacy also empty: return nil (existing behavior — falls through to `DefaultAgentRoles()` convention in appsetup)
- Update the call site at line ~1349 (`runOrgInstall`) to pass `ctx` and `printer` to the new signature

**Handling duplicate roles:** `DiscoverRemoteAgents` can return multiple entries with the same role (e.g., `code.yaml` and `fix.yaml` both have `role: coder`). When building the `map[role]slug`, the first entry wins (sorted order: `code.yaml` before `fix.yaml`). This matches the existing behavior where `AgentSlugs()` returns one slug per role. Log at debug level when a duplicate role is encountered.

**Modify `internal/cli/admin_test.go`:**

- Test: config repo has harness wrappers with role/slug → `loadKnownSlugs` returns slugs from harness files, no deprecation warning
- Test: config repo has no `harness/` dir but has `config.yaml` with `agents:` → falls back, emits deprecation warning
- Test: config repo has harness wrappers WITHOUT role/slug (legacy format) → falls back to `agents:` block
- Test: neither harness files nor `agents:` block → returns nil

**After merge:** `loadKnownSlugs` prefers harness wrapper files in the config repo. Existing installs with only `config.yaml` agents: block continue to work but see a deprecation notice.

**Depends on:** PR 2

---

## PR 5: Migrate uninstall flows to harness-first discovery

**Scope:** Change `runUninstall` and `runGitHubUninstall` to discover agent slugs from harness wrapper files before falling back to the `agents:` block.

**Modify `internal/cli/admin.go` — `runUninstall` (line ~1600):**

- Before reading `parsedCfg.Agents`, call `harness.DiscoverRemoteAgents(ctx, client, owner, configRepo, ref)`
- If harness discovery returns results: build slug list from `AgentInfo.Slug` values
- If harness discovery returns empty: fall back to `parsedCfg.Agents` (existing behavior) with deprecation notice
- If both empty: fall back to `DefaultAgentRoles()` convention (existing behavior)
- The three-tier fallback chain is:
  ```
  harness files → config.yaml agents: block → DefaultAgentRoles() convention
  ```

**Modify `internal/cli/github.go` — `runGitHubUninstall` (line ~822):**

- Same three-tier fallback chain as `runUninstall`
- Extract a shared helper to avoid duplicating the fallback logic:
  ```go
  func discoverAgentSlugs(ctx context.Context, client forge.Client, owner, configRepo, ref string, cfg *config.OrgConfig, printer *ui.Printer) []string
  ```
  This helper encapsulates the three-tier discovery and deprecation warning. Both `runUninstall` and `runGitHubUninstall` call it.

**Create `internal/cli/discover_slugs.go`:**

- `discoverAgentSlugs` helper function (unexported)
- Returns `[]string` (slug list, deduplicated)
- Logs which discovery tier was used at debug level
- Emits deprecation warning when falling back to `agents:` block

**Tests:**

- `internal/cli/admin_test.go`: uninstall with harness wrappers → uses harness slugs
- `internal/cli/admin_test.go`: uninstall with only `agents:` block → falls back, deprecation warning
- `internal/cli/github_test.go`: same scenarios for `runGitHubUninstall`
- Both: empty harness and empty agents → falls back to `DefaultAgentRoles()` convention

**After merge:** Uninstall flows prefer harness wrapper files for agent discovery. Existing installations without harness wrappers continue to work via fallback.

**Depends on:** PR 2

---

## PR 6: Make OrgConfig.Agents optional with deprecation notice

**Scope:** Allow `config.yaml` to omit the `agents:` block entirely. When present, log a deprecation notice during config load. The install flow continues to dual-write (Phase 4 stops it).

**Modify `internal/config/config.go`:**

- Change `Agents` yaml tag from `yaml:"agents"` to `yaml:"agents,omitempty"`
- `AgentSlugs()` already handles nil `Agents` (returns empty map) — verify with a test
- Add `HasAgentsBlock() bool` — returns `len(c.Agents) > 0`. Used by CLI commands to decide whether to emit a deprecation notice.

**Modify `internal/config/config_test.go`:**

- Test: config YAML without `agents:` block → `OrgConfig.Agents` is nil, `AgentSlugs()` returns empty map
- Test: config YAML with empty `agents: []` → `AgentSlugs()` returns empty map
- Test: config YAML with populated `agents:` → existing behavior unchanged
- Test: `HasAgentsBlock()` returns correct values for each case
- Test: serializing `OrgConfig` with nil `Agents` omits the `agents:` key from YAML output

**Modify `internal/cli/admin.go`:**

- After loading config in `runOrgInstall`: if `cfg.HasAgentsBlock()`, emit deprecation notice:
  ```
  ⚠ config.yaml contains an agents: block. Agent identity is now managed in harness files.
    The agents: block will be removed in a future version.
    Run 'fullsend install' to migrate.
  ```
- The install flow still writes the `agents:` block (dual-write continues). Phase 4 will remove it.

**Modify `internal/cli/admin.go` — `runPerRepoInstall`:**

- Check for `cfg.HasAgentsBlock()` and emit the same deprecation notice if present.

**After merge:** `config.yaml` can omit `agents:` without errors. When present, a deprecation notice encourages migration. Install continues dual-writing for backward compatibility.

**Depends on:** PRs 4, 5 (consumers migrated before making the field optional)

---

## Verification

After all PRs merge, verify Phase 3 end-to-end:

1. `make go-test` — all new and existing tests pass
2. `make go-vet` — no issues
3. `make lint` — passes
4. **Lint diagnostics:** `fullsend run` on a harness without `role` emits a warning but succeeds
5. **Lint diagnostics:** `fullsend lock` and `fullsend lock --all` emit warnings for harnesses missing `role`
6. **No warning for valid harnesses:** `fullsend run` on a harness with `role` produces no lint output
7. **Remote discovery:** `loadKnownSlugs` reads role/slug from remote harness wrapper files in the config repo
8. **Remote discovery fallback:** when no harness files exist, `loadKnownSlugs` falls back to `config.yaml` `agents:` block with deprecation notice
9. **Uninstall discovery:** `runUninstall` discovers agent slugs from remote harness files
10. **Uninstall fallback:** when no harness files exist, uninstall falls back to `agents:` block then `DefaultAgentRoles()`
11. **OrgConfig optional agents:** config.yaml without `agents:` block loads without error; `AgentSlugs()` returns empty map
12. **OrgConfig omitempty:** serializing `OrgConfig` with nil `Agents` omits the key from YAML output
13. **Deprecation notice:** loading config.yaml with an `agents:` block emits deprecation warning
14. **Backward compat:** existing config.yaml with `agents:` block continues to work identically (dual-write still active, all consumers still check `agents:` as fallback)
15. **Dual-write intact:** `fullsend install` still writes both harness wrapper files and `config.yaml` `agents:` block

---

## Future: Phase 4 (Remove)

Phase 4 is not planned in detail here, but its scope is:

- Require `role` in `Validate()` (move from `Lint()` warning to hard error)
- ~~Stop writing `agents:` block during install (remove the dual-write from `HarnessWrappersLayer` and config generation)~~ *(`HarnessWrappersLayer` removed — PR #5425)*
- Remove `OrgConfig.Agents` field and `AgentSlugs()` method
- Remove `loadKnownSlugsLegacy` and the fallback tier in `discoverAgentSlugs`
- Remove `HasAgentsBlock()` and all deprecation notice code
- Consider config schema version bump to "v2" (per ADR open question)
- Audit all consumers (2-3 PRs estimated)
