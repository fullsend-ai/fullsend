# Implementation Plan: ADR-0045 Forge-Portable Harness Schema — Phase 4 (Remove)

## Context

Phase 3 (shipped) completed the "Deprecate" milestone: `Lint()` warns when `role` is missing from a harness file. `loadKnownSlugs()` and `discoverAgentSlugs()` both prefer harness wrapper files, falling back to the `config.yaml` `agents:` block with a deprecation notice. `OrgConfig.Agents` uses `omitempty` so config.yaml can omit the `agents:` block entirely. `HasAgentsBlock()` reports whether the legacy block is present.

Phase 4 completes the "Remove" milestone from the ADR migration path. Specifically:

1. **Require `role` in `Validate()`** -- move from `Lint()` warning to hard error. Harnesses without `role` will fail to load.

2. **Stop writing the `agents:` block during install** -- ✅ Shipped (#2447). Removed the `agents` parameter from `NewOrgConfig()`. The `ConfigRepoLayer` writes config.yaml without an `agents:` block.

3. **Remove `OrgConfig.Agents` field and `AgentSlugs()` method** -- the field and its accessor are dead code after the dual-write stops and all consumers migrate.

4. **Remove `loadKnownSlugsLegacy` and the fallback tier in `discoverAgentSlugs`** -- harness-first discovery becomes the only path. The legacy config.yaml fallback is deleted.

5. **Remove `HasAgentsBlock()` and all deprecation notice code** -- with the `agents:` block gone, deprecation checks are unnecessary.

6. **Config schema version: stay on v1** -- removing `agents:` does not warrant a v2 bump (see rationale below).

ADR: `docs/ADRs/0045-forge-portable-harness-schema.md`
Phase 1 plan: `docs/plans/adr-0045-forge-portable-harness-phase1.md`
Phase 2 plan: `docs/plans/adr-0045-forge-portable-harness-phase2.md`
Phase 3 plan: `docs/plans/adr-0045-forge-portable-harness-phase3.md`

### Relationship to Phase 3

| Phase 3 artifact | Phase 4 action |
|---|---|
| `Lint()` warning for missing `role` | Promote to hard error in `Validate()` |
| `loadKnownSlugsLegacy` fallback | Delete function, remove fallback tier |
| `discoverAgentSlugs` three-tier fallback | Remove tier 2 (config.yaml `agents:` block) |
| `OrgConfig.Agents` with `omitempty` | Remove field entirely |
| `AgentSlugs()` method | Remove method |
| `HasAgentsBlock()` method | Remove method |
| Deprecation notice in `runOrgInstall` | Remove notice code |
| Dual-write in `runInstall` / `runGitHubSetup` | ✅ Stop passing agents to `NewOrgConfig` (#2447) |
| `HarnessWrappersLayer` generating role/slug | Unchanged -- remains the sole source of agent identity |

### Config schema version: stay on v1

The ADR asks whether removing `agents:` warrants a v2 schema. The recommendation is to stay on v1 for the following reasons:

- **The change is backward-compatible on the read path.** Phase 3 already made `Agents` use `omitempty`. Existing configs without `agents:` parse successfully today. No consumer requires the field to be present -- all have harness-first fallbacks.
- **The change is backward-compatible on the write path.** `NewOrgConfig` no longer accepts or populates the field. `Marshal()` with `omitempty` already omits nil/empty slices.
- **A v2 bump would break all existing installations.** `OrgConfig.Validate()` rejects `Version != "1"`. A v2 would require either accepting both versions or migrating every deployed config.yaml, adding complexity for no user-facing benefit.
- **The v1 schema contract (ADR-0011) defines minimum required fields, not an exhaustive field list.** Optional fields with `omitempty` can be added or removed without a version bump.

If a future change requires breaking the v1 contract (e.g., removing `dispatch.platform` or changing `repos` structure), that is the appropriate time for a v2 bump.

### What Phase 4 does NOT do

- Does NOT add new harness schema features (forge blocks, base composition improvements)
- Does NOT change `PerRepoConfig` -- per-repo mode does not use the `agents:` block
- Does NOT remove `AgentEntry` from `config.go` -- it is still used by `AgentCredentials` in `internal/layers/secrets.go` for the install flow's credential passing. `AgentEntry` represents credentials obtained during app setup, not config.yaml schema.
- Does NOT change harness loading pipelines (`Load`, `LoadWithOpts`, `LoadWithBase`)
- Does NOT remove `DefaultAgentRoles()` or `ValidRoles()` -- these are used for role validation and app setup, independent of the `agents:` block
- Does NOT remove the `forge:` section or `base:` field infrastructure (those are permanent schema additions)

### Ordering: "require role" and "remove agents block" are independent

The two main workstreams touch different packages:

- **Require role** modifies `internal/harness/harness.go` (`Validate()`) and `internal/harness/lint.go` (remove lint rule). No config or CLI changes.
- **Remove agents block** modifies `internal/config/config.go`, `internal/cli/admin.go`, `internal/cli/github.go`, `internal/cli/discover_slugs.go`, and `internal/layers/harnesswrappers.go`.

These are independent and can proceed in parallel. PR 1 (require role) has no dependency on PR 2/3/4 (remove agents infrastructure).

### Consumer audit

Every consumer of the removed code, and the action taken:

| Consumer | Location | Current behavior | Phase 4 action |
|---|---|---|---|
| `OrgConfig.Agents` field | `internal/config/config.go:86` | `yaml:"agents,omitempty"` | Remove field |
| `AgentSlugs()` method | `internal/config/config.go:259` | Returns `map[role]slug` from `Agents` | Remove method |
| `HasAgentsBlock()` method | `internal/config/config.go:270` | Returns `len(c.Agents) > 0` | Remove method |
| `NewOrgConfig` agents param | `internal/config/config.go:117` | Accepts `[]AgentEntry`, sets `cfg.Agents` | ✅ Remove parameter, stop setting field (#2447) |
| `NewOrgConfig` caller: `runDryRun` | `internal/cli/admin.go:1196` | Passes `nil` for agents | ✅ Remove agents arg (#2447) |
| `NewOrgConfig` caller: `runInstall` | `internal/cli/admin.go:1513` | Passes agents built from `agentCreds` | ✅ Remove agents arg (#2447) |
| `NewOrgConfig` caller: `runUninstall` | `internal/cli/admin.go:1659` | Passes `nil` for agents | ✅ Remove agents arg (#2447) |
| `NewOrgConfig` caller: `runAnalyze` | `internal/cli/admin.go:1800` | Passes `nil` for agents | ✅ Remove agents arg (#2447) |
| `NewOrgConfig` caller: `runGitHubSetup` (dry-run) | `internal/cli/github.go:437` | Passes `dummyAgents` | ✅ Remove agents arg (#2447) |
| `NewOrgConfig` caller: `runGitHubSetup` (real) | `internal/cli/github.go:487` | Passes `agents` from creds | ✅ Remove agents arg (#2447) |
| `loadKnownSlugsLegacy` | `internal/cli/admin.go:2064` | Reads `cfg.AgentSlugs()` from config.yaml | Remove function |
| `loadKnownSlugs` legacy fallback | `internal/cli/admin.go:2056` | Calls `loadKnownSlugsLegacy` if harness discovery empty | Remove fallback call |
| `discoverAgentSlugs` tier 2 | `internal/cli/discover_slugs.go:49-66` | Falls back to `cfg.Agents` | Remove fallback block |
| `discoverAgentSlugs` `cfg` parameter | `internal/cli/discover_slugs.go:23` | Accepts `*config.OrgConfig` for legacy fallback | Remove parameter |
| `discoverAgentSlugs` caller: `runUninstall` | `internal/cli/admin.go:1610` | Passes `parsedCfg` | Stop passing config |
| `discoverAgentSlugs` caller: `runGitHubUninstall` | `internal/cli/github.go:834` | Passes `parsedCfg` | Stop passing config |
| `Lint()` role warning | `internal/harness/lint.go:43-48` | Warns when `role == ""` | Remove (superseded by `Validate()` error) |
| Lint callers: `run.go`, `lock.go` | `internal/cli/run.go:345`, `internal/cli/lock.go:207` | Print lint diagnostics | Remove role-specific diagnostic handling (if no other lint rules remain, Lint() still exists but returns nil) |

## PR Dependency Graph

```
PR 1 (require role in Validate)  [independent]

PR 2 (remove agents from NewOrgConfig + ConfigRepoLayer) ──> PR 4 (remove OrgConfig.Agents field)
                                                                      │
PR 3 (remove legacy discovery fallbacks) ─────────────────────────────┘
```

PRs 1, 2, and 3 can all start in parallel. PR 4 depends on PRs 2 and 3 (all callers of `OrgConfig.Agents`, `AgentSlugs()`, and `HasAgentsBlock()` must be migrated before the fields are removed).

---

## PR 1: Require `role` in `Validate()`

**Scope:** Promote missing `role` from a `Lint()` warning to a `Validate()` hard error. Remove the lint rule (which becomes redundant). Update tests.

**Risk note:** This is a breaking change for any harness file that lacks `role:`. Phase 1 PR 6 added `role:` to all scaffold templates. Phase 2 PR 4 generates harness wrappers with `role:`. Phase 3's `Lint()` has been warning users. The only harnesses that would break are user-maintained files that were never updated despite warnings. The fix is a single line: add `role: <rolename>`.

**Modify `internal/harness/harness.go` -- `Validate()`:**
- After the existing `h.Role != ""` validation block (line ~323), add:
  ```go
  if h.Role == "" {
      return fmt.Errorf("role field is required")
  }
  ```
- The existing role pattern validation (lines 323-329) stays as-is -- it only runs when `h.Role != ""`. Restructure so the empty check comes first:
  ```go
  if h.Role == "" {
      return fmt.Errorf("role field is required")
  }
  if !validRoleName.MatchString(h.Role) {
      return fmt.Errorf("role %q contains invalid characters ...", h.Role)
  }
  if strings.Contains(h.Role, "--") {
      return fmt.Errorf("role %q must not contain double hyphens", h.Role)
  }
  ```

**Modify `internal/harness/lint.go` -- `Lint()`:**
- Remove the `h.Role == ""` diagnostic block (lines 43-48). `Validate()` now catches this as a hard error before `Lint()` is ever called.
- `Lint()` still exists and returns `nil` when no diagnostics are found. Future lint rules (missing slug, single-forge informational, stale base SHA) can be added here without changing any interface.

**Modify `internal/harness/lint_test.go`:**
- Remove or update the "harness without role -> one warning diagnostic" test case. Replace with a test that `Lint()` returns nil for a valid harness (role is now always set on a valid harness).

**Modify `internal/harness/harness_test.go` (or relevant test file):**
- Add test: harness YAML without `role:` -> `Load()` returns error containing "role field is required"
- Add test: harness YAML with `role: triage` -> `Load()` succeeds
- Update any existing tests that load harnesses without `role:` -- add `role:` to their test YAML fixtures

**Modify scaffold test fixtures:**
- Scan test files in `internal/harness/` for inline YAML that omits `role:`. Add `role: test` (or appropriate value) to each fixture. This is the bulk of the test update work.

**Check `internal/cli/run.go` and `internal/cli/lock.go`:**
- The `Lint()` call sites (run.go:345, lock.go:207) iterate `h.Lint()` and print diagnostics. Since the role warning is removed from `Lint()`, these call sites still work -- they just emit nothing for the role case. No code changes needed unless there are no other lint rules, in which case `Lint()` always returns nil and the loop is a no-op. Keep the call sites for future lint rules.

**After merge:** Harnesses without `role:` fail to load. All scaffold templates and generated wrappers already have `role:`. Existing deployments with user-maintained harnesses see a clear error with the fix: add `role: <rolename>`.

---

## PR 2: Stop writing `agents:` block during install — ✅ Shipped (#2447)

**Scope:** Remove the `agents` parameter from `NewOrgConfig()`. All `NewOrgConfig` callers stop building and passing agent entries. The `ConfigRepoLayer` writes config.yaml without an `agents:` block. The `HarnessWrappersLayer` remains unchanged -- it is now the sole source of agent identity.

All items below were completed in #2447:

- ✅ Removed `agents []AgentEntry` parameter from `NewOrgConfig` signature
- ✅ Removed `Agents: agents` from struct literal
- ✅ Updated all `NewOrgConfig` callers in `admin.go` (`runDryRun`, `runInstall`, `runUninstall`, `runAnalyze`)
- ✅ Updated all `NewOrgConfig` callers in `github.go` (`runGitHubSetup` dry-run and real paths)
- ✅ Removed agent entry construction code (`dummyAgents`, `agents` slices built from `agentCreds`)
- ✅ Updated all test files (`config_test.go`, `admin_test.go`, `github_test.go`, `configrepo_test.go`)

---

## PR 3: Remove legacy discovery fallbacks

**Scope:** Remove `loadKnownSlugsLegacy`, simplify `loadKnownSlugs`, remove the config.yaml fallback tier from `discoverAgentSlugs`, and remove all deprecation notice code.

### `internal/cli/admin.go` -- `loadKnownSlugs` and `loadKnownSlugsLegacy`

**Delete `loadKnownSlugsLegacy`** (lines 2063-2074): the entire function is removed.

**Simplify `loadKnownSlugs`** (lines 2028-2061):
- Remove the fallback call to `loadKnownSlugsLegacy` and the deprecation warning.
- The function now only does harness-first discovery. If harness discovery returns empty, it returns nil (the caller handles its own fallback to `DefaultAgentRoles()` convention).
- Updated function:
  ```go
  func loadKnownSlugs(ctx context.Context, client forge.Client, org, configRepo, ref string, printer *ui.Printer) map[string]string {
      agents, err := harness.DiscoverRemoteAgents(ctx, client, org, configRepo, ref)
      if err != nil {
          printer.StepWarn(fmt.Sprintf("harness discovery: %v", err))
      }
      if len(agents) == 0 {
          return nil
      }
      slugs := make(map[string]string, len(agents))
      seen := make(map[string]bool, len(agents))
      for _, a := range agents {
          if a.Role == "" && a.Slug == "" {
              continue
          }
          if a.Role == "" || a.Slug == "" {
              printer.StepWarn(fmt.Sprintf("harness %s has role=%q slug=%q; both must be set", a.Filename, a.Role, a.Slug))
              continue
          }
          if seen[a.Role] {
              printer.StepInfo(fmt.Sprintf("duplicate role %q in harness file %s, using first occurrence", a.Role, a.Filename))
              continue
          }
          seen[a.Role] = true
          slugs[a.Role] = a.Slug
      }
      if len(slugs) > 0 {
          return slugs
      }
      return nil
  }
  ```

### `internal/cli/discover_slugs.go` -- `discoverAgentSlugs`

**Remove the `cfg *config.OrgConfig` parameter** and the tier 2 fallback block (lines 49-66):
```go
func discoverAgentSlugs(ctx context.Context, client forge.Client, owner, configRepo, ref, appSet string, printer *ui.Printer) []string {
    agents, err := harness.DiscoverRemoteAgents(ctx, client, owner, configRepo, ref)
    if err != nil {
        printer.StepWarn(fmt.Sprintf("some harness files could not be read: %v", err))
    }
    if len(agents) > 0 {
        seen := make(map[string]bool, len(agents))
        var slugs []string
        for _, a := range agents {
            slug := a.Slug
            if slug == "" && a.Role != "" {
                slug = appsetup.AppSlug(appSet, a.Role)
            }
            if slug == "" {
                continue
            }
            if !seen[slug] {
                seen[slug] = true
                slugs = append(slugs, slug)
            }
        }
        if len(slugs) > 0 {
            return slugs
        }
    }
    return nil
}
```

**Update callers:**

- `internal/cli/admin.go` -- `runUninstall` (line ~1610): remove `parsedCfg` argument:
  ```go
  agentSlugs = discoverAgentSlugs(ctx, client, org, forge.ConfigRepoName, "main", appSet, printer)
  ```
  Also remove the `parsedCfg` variable and the code that parses config.yaml to populate it (lines ~1599-1607), since `parsedCfg` is no longer used by `discoverAgentSlugs`. Note: `runUninstall` still reads config.yaml for `configMode` and `enrolledRepos` -- only the `parsedCfg` usage in `discoverAgentSlugs` is removed. Restructure the config parsing so it still sets `configMode` and `enrolledRepos` but does not build `parsedCfg` as a separate variable passed to `discoverAgentSlugs`.

- `internal/cli/github.go` -- `runGitHubUninstall` (line ~834): remove `parsedCfg` argument:
  ```go
  agentSlugs = discoverAgentSlugs(ctx, client, org, forge.ConfigRepoName, "main", appSet, printer)
  ```
  Similarly, the `parsedCfg` variable (line ~826) is only used for `discoverAgentSlugs`. Remove it and the associated parsing code. `runGitHubUninstall` does not use `configMode` or `enrolledRepos`, so the entire config parsing block can be deleted.

### Remove deprecation notice code

- `internal/cli/admin.go`: search for any `HasAgentsBlock()` calls and associated deprecation notice printing. Remove them. (Based on the Phase 3 plan, these would be in `runOrgInstall` and `runPerRepoInstall` -- verify at implementation time.)

### Test updates

**Modify `internal/cli/admin_test.go`:**
- Remove or update tests for `loadKnownSlugsLegacy` behavior
- Update `loadKnownSlugs` tests: remove test cases that verify fallback to `agents:` block. Keep tests for harness-first discovery and empty-result behavior.

**Modify `internal/cli/discover_slugs_test.go`:**
- Remove test cases: `TestDiscoverAgentSlugs_FallsBackToAgentsBlock`, `TestDiscoverAgentSlugs_ConfigAgentWithoutSlug_DerivesFromRole`, `TestDiscoverAgentSlugs_EmptyAgentsBlock_ReturnsNil`
- Update remaining test cases to not pass a `cfg` argument
- Keep: `TestDiscoverAgentSlugs_HarnessFirst`, `TestDiscoverAgentSlugs_HarnessWithoutSlug_DerivesFromRole`, `TestDiscoverAgentSlugs_NeitherSource_ReturnsNil`, `TestDiscoverAgentSlugs_DeduplicatesSlugs`, `TestDiscoverAgentSlugs_PartialError_UsesValidAgents`

**After merge:** All legacy discovery paths are removed. Agent slug discovery uses harness wrapper files exclusively, with `DefaultAgentRoles()` as the ultimate fallback in the caller (unchanged -- this is the tier 3 fallback that already exists in `runUninstall` and `runGitHubUninstall`).

**Depends on:** No dependency on PR 1 or PR 2. Can be done in parallel.

---

## PR 4: Remove `OrgConfig.Agents` field, `AgentSlugs()`, and `HasAgentsBlock()`

**Scope:** Delete dead code from `internal/config/config.go`. All consumers have been migrated by PRs 2 and 3.

**Modify `internal/config/config.go`:**

- Remove `Agents []AgentEntry` from `OrgConfig` struct (line 86)
- Remove `AgentSlugs()` method (lines 258-265)
- Remove `HasAgentsBlock()` method (lines 267-272)
- Keep `AgentEntry` type (lines 20-24) -- it is still used by `layers.AgentCredentials` for passing app credentials through the install flow. `AgentEntry` describes credentials obtained during app setup, not config.yaml schema.

**Modify `internal/config/config_test.go`:**

- Remove `TestOrgConfigAgentSlugs` (line ~224)
- Remove any tests for `HasAgentsBlock()`
- Remove test cases that verify `Agents` serialization/deserialization
- Add a test: parse config YAML that has an `agents:` block -> verify it parses without error (the field is simply ignored via `yaml.Unmarshal` since it's not on the struct). This is important for backward compatibility: old config.yaml files with `agents:` must still load.

**Backward compatibility note:** When `OrgConfig.Agents` is removed from the struct, `yaml.Unmarshal` silently ignores the `agents:` key in YAML input. This means existing config.yaml files with an `agents:` block will still parse successfully. Marshaling (`cfg.Marshal()`) will not include the key. This is the desired behavior -- old configs work, new configs are clean.

**After merge:** `OrgConfig` no longer references agents. The config schema is purely operational (version, dispatch, inference, defaults, repos, allowed_remote_resources, create_issues).

**Depends on:** PRs 2 and 3 (all consumers removed).

---

## Verification

After all PRs merge, verify Phase 4 end-to-end:

1. `make go-test` -- all new and existing tests pass
2. `make go-vet` -- no issues
3. `make lint` -- passes
4. **Role required:** `fullsend run` on a harness without `role:` fails with "role field is required"
5. **Role required:** `fullsend run` on a harness with `role: triage` succeeds
6. **Config output:** `fullsend admin install --dry-run` shows config.yaml without `agents:` key
7. **Config output:** `fullsend admin install` writes config.yaml without `agents:` key
8. **Harness wrappers unchanged:** `fullsend admin install` still generates harness wrappers with `base:`, `role:`, `slug:`
9. **Slug discovery:** `loadKnownSlugs` discovers slugs from remote harness files
10. **Slug discovery:** no deprecation warning is emitted (the legacy path is gone)
11. **Uninstall discovery:** `runUninstall` and `runGitHubUninstall` discover agent slugs from harness files
12. **Uninstall fallback:** when no harness files exist, uninstall falls back to `DefaultAgentRoles()` convention (tier 3, unchanged)
13. **Backward compat -- config parse:** existing config.yaml with `agents:` block parses without error (`yaml.Unmarshal` ignores the unknown field)
14. **Backward compat -- config write:** config.yaml marshaled from `OrgConfig` does not contain `agents:` key
15. **No code references:** `grep -rn 'AgentSlugs\|HasAgentsBlock\|loadKnownSlugsLegacy' --include='*.go'` returns no results (excluding test fixtures and this plan)
16. **Lint still works:** `h.Lint()` returns nil for valid harnesses (the role warning is gone, no other warnings currently). Lint call sites in `run.go` and `lock.go` are still present for future lint rules.
