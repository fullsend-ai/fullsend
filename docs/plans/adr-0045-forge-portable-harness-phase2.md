# Implementation Plan: ADR-0045 Forge-Portable Harness Schema — Phase 2 (Adopt)

## Context

Phase 1 (shipped) added `role`, `slug`, `base`, and `forge:` to the harness YAML schema as optional fields with full merge logic, base composition, forge resolution, and pipeline integration. All existing harnesses continue to work unchanged. The scaffold harness templates already contain `role` and `slug` fields.

Phase 2 completes the "Adopt" milestone from the ADR migration path: existing infrastructure transitions to use the new schema fields. Specifically:

1. **`fullsend install` writes `role`/`slug` into harness files** -- today the admin install flow writes agent identity only to `config.yaml`'s `agents:` block. Phase 2 makes install also write `role`/`slug` into the harness files themselves, establishing the harness as the source of truth (while keeping the `agents:` block for backward compatibility until Phase 4).

2. **Thin `base:` wrappers replace full scaffold copies** -- today `fullsend install` delivers harness files as static embedded copies. Phase 2 changes install to generate thin wrapper harnesses that reference upstream scaffold harnesses via URL (with `#sha256=...` integrity hash), using the same `base` composition from Phase 1. Orgs customize by overriding fields in the wrapper instead of editing the upstream copy.

3. **GitHub-specific fields move into `forge.github:` blocks** -- today all scaffold harness templates have `pre_script`, `post_script`, and `runner_env` at the top level, making them implicitly GitHub-only. Phase 2 moves these into `forge.github:` blocks, making the templates structurally portable even though only GitHub is supported today.

4. **`harness.DiscoverAgents()` enables harness-based agent discovery** -- today agent inventory is read from `config.yaml`'s `agents:` block via `OrgConfig.AgentSlugs()`. Phase 2 adds a function that scans `harness/*.yaml` files for `role`/`slug`, providing a parallel discovery path. Consumers are NOT migrated yet (that is Phase 3).

ADR: `docs/ADRs/0045-forge-portable-harness-schema.md`
Phase 1 plan: `docs/plans/adr-0045-forge-portable-harness-phase1.md`

### Relationship to Phase 1

Phase 2 builds on Phase 1's deliverables:

| Phase 1 artifact | Phase 2 usage |
|---|---|
| `Harness.Role`, `Harness.Slug` fields | Install writes these into generated harness wrappers |
| `Harness.Base` field + `LoadWithBase()` | Generated wrappers use `base:` to reference upstream scaffold harnesses |
| `ForgeConfig` struct + `ResolveForge()` | Scaffold templates move GitHub fields into `forge.github:` blocks |
| `LoadRaw()` | `DiscoverAgents()` uses it to scan harness files without full validation |
| `Dependency` type from `LoadWithBase()` | `fullsend lock` records `base` URL deps for generated wrappers |

### Relationship to ADR-0038 (Universal Harness Access)

Phase 2 generates `base:` URLs pointing to upstream scaffold harnesses. These URLs follow the same rules as all ADR-0038 URL resources:

- HTTPS-only with mandatory `#sha256=...` integrity hash
- Must be covered by `allowed_remote_resources` in `config.yaml`
- Fetched via the SSRF-hardened `internal/fetch` layer
- Cached in `.fullsend-cache/` and recorded in `lock.yaml` via `fullsend lock`

The base URL format uses `raw.githubusercontent.com` with the release tag SHA:

```
https://raw.githubusercontent.com/fullsend-ai/fullsend/<commit-sha>/internal/scaffold/fullsend-repo/harness/triage.yaml#sha256=<hash>
```

The `<commit-sha>` and `<hash>` are computed at build time or during install from the current CLI release. `fullsend lock` pins them in `lock.yaml`.

### Open questions resolved by this plan

**Slug derivation convention (ADR open question):** Phase 2 does NOT auto-derive slugs from roles. The install flow already knows the actual slug from GitHub's manifest response (or from `loadKnownSlugs()`). Harness wrappers receive the real slug. This avoids introducing an implicit `<org>-<role>` naming contract.

**config.yaml `agents:` block coexistence:** Phase 2 writes agent identity to BOTH locations -- harness files AND config.yaml. The `agents:` block remains the canonical source for all existing consumers (`loadKnownSlugs`, `runUninstall`, `SecretsLayer`). Phase 3 migrates consumers to harness discovery; Phase 4 removes the `agents:` block.

**`fullsend lock` iteration:** Today `fullsend lock` takes a single agent name. Phase 2 adds `fullsend lock --all` to iterate all harness files in the directory, which is needed because install generates wrappers with `base:` URLs that all need locking.

## PR Dependency Graph

```
PR 1 (DiscoverAgents) ──────────────────────────────────────────────┐
                                                                    │
PR 2 (forge.github in scaffold templates) ──────────────────────────┤
                                                                    │
PR 3 (base URL generation + scaffold hashing) ──> PR 4 (install) ──┤
                                                                    │
PR 5 (fullsend lock --all) ────────────────────────────────────────>│
                                                                    │
                                                                    └──> PR 6 (integration tests + verification)
```

PRs 1, 2, 3, 5 can start in parallel. PR 4 depends on PR 3 (needs the base URL builder). PR 6 depends on all others for end-to-end integration verification.

---

## PR 1: `harness.DiscoverAgents()` -- harness-based agent inventory

**Scope:** Add a function that scans a harness directory and returns agent identity (role, slug) from each YAML file. No consumers are migrated -- this is pure library code for Phase 3.

**Create `internal/harness/discover.go`:**

- `AgentInfo` struct:
  ```go
  type AgentInfo struct {
      Role     string // from harness role: field
      Slug     string // from harness slug: field
      Filename string // e.g. "triage.yaml"
      Path     string // absolute path to the harness file
  }
  ```
- `DiscoverAgents(dir string) ([]AgentInfo, error)`:
  - Globs `filepath.Join(dir, "*.yaml")` and `filepath.Join(dir, "*.yml")`
  - For each file, calls `LoadRaw(path)` (unmarshal only, no validation)
  - Extracts `h.Role` and `h.Slug`; skips files where both are empty (not harness identity files, or legacy harnesses without role/slug)
  - Returns sorted by `Role` for deterministic output
  - Errors on individual files are collected and returned as a multi-error (one bad YAML file should not prevent discovery of others). This partial-result semantic is intentional: discovery is a read-only inventory operation where returning what we can find is more useful than failing entirely. `lock --all` (PR 5) uses similar partial-progress semantics: successfully resolved harnesses are saved before returning the error, so they don't need to be re-resolved on retry.
  - Does NOT resolve `base:` chains -- reads only the top-level `role`/`slug` from each file. This is correct because generated wrappers set `role`/`slug` at the top level (not inherited from base).

**Create `internal/harness/discover_test.go`:**
- Directory with multiple harness files -> returns sorted AgentInfo list
- Harness without role/slug -> skipped
- Harness with only role (no slug) -> included (role is the primary key)
- Malformed YAML -> included in multi-error, other files still returned
- Empty directory -> empty list, no error
- Non-existent directory -> nil, nil (not an error -- matches LoadProviderDefs convention)
- `.yml` extension -> discovered alongside `.yaml`

**Known complexity for Phase 3 consumers:** `DiscoverAgents` can return multiple entries with the same `role` value (e.g., `code.yaml` and `fix.yaml` both have `role: coder`). Phase 3 consumers that build role-to-slug maps must handle this — likely by using the filename as a disambiguator or by grouping entries by role.

**After merge:** `DiscoverAgents` exists as a tested library function. No callers. Phase 3 will wire it into `loadKnownSlugs()` and other consumers.

---

## PR 2: Move GitHub-specific fields into `forge.github:` in scaffold templates

**Scope:** Restructure all scaffold harness templates to use `forge.github:` blocks for platform-specific fields. The templates remain functionally identical when loaded with `--forge github` (which is the only supported platform today). This is a pure template change -- no Go code changes.

**Design note:** The `forge.github:` blocks contain `pre_script`, `post_script`, and `runner_env` entries that are GitHub-specific. Platform-neutral `runner_env` keys (e.g., `FULLSEND_OUTPUT_SCHEMA`, `FULLSEND_OUTPUT_FILE`, `TARGET_BRANCH`) remain at the top level as shared defaults, merged with the forge-specific keys at runtime via the Phase 1 `ResolveForge` logic. The `skills` field stays at the top level because skills are agent instructions, not forge API integrations (even skills that reference `gh` CLI are invoked inside the sandbox where the CLI is pre-installed regardless of forge).

### Per-template changes

**`internal/scaffold/fullsend-repo/harness/triage.yaml`:**
- Move to `forge.github:`: `pre_script`, `post_script`
- Move to `forge.github.runner_env:`: `GITHUB_ISSUE_URL`, `GH_TOKEN`
- Keep at top level `runner_env:`: `FULLSEND_OUTPUT_SCHEMA`
- Keep at top level: `agent`, `doc`, `model`, `image`, `policy`, `role`, `slug`, `skills`, `host_files`, `timeout_minutes`, `validation_loop`

**`internal/scaffold/fullsend-repo/harness/code.yaml`:**
- Move to `forge.github:`: `pre_script`, `post_script`
- Move to `forge.github.runner_env:`: `REPO_FULL_NAME`, `ISSUE_NUMBER`, `REPO_DIR` (value `${GITHUB_WORKSPACE}/target-repo`)
- `PUSH_TOKEN`, `PUSH_TOKEN_SOURCE`: auto-minted by `mintAgentToken()` when `--mint-url` is provided- Keep at top level `runner_env:`: `TARGET_BRANCH`
- Keep at top level: `agent`, `doc`, `model`, `image`, `policy`, `role`, `slug`, `skills`, `plugins`, `host_files`, `timeout_minutes`

**`internal/scaffold/fullsend-repo/harness/review.yaml`:**
- Move to `forge.github:`: `pre_script`, `post_script`
- Move to `forge.github.runner_env:`: `REPO_FULL_NAME`, `PR_NUMBER`, `GITHUB_PR_URL`
- `REVIEW_TOKEN`: auto-minted by `mintAgentToken()` when `--mint-url` is provided- Keep at top level `runner_env:`: `FULLSEND_OUTPUT_SCHEMA`
- Keep at top level: `agent`, `doc`, `model`, `image`, `policy`, `role`, `slug`, `skills`, `host_files`, `timeout_minutes`, `validation_loop`

**`internal/scaffold/fullsend-repo/harness/fix.yaml`:**
- Move to `forge.github:`: `pre_script`, `post_script`
- Move to `forge.github.runner_env:`: `REPO_FULL_NAME`, `PR_NUMBER`, `REPO_DIR` (value `${GITHUB_WORKSPACE}/target-repo`)
- `PUSH_TOKEN`, `PUSH_TOKEN_SOURCE`: auto-minted by `mintAgentToken()` when `--mint-url` is provided- Keep at top level `runner_env:`: `TARGET_BRANCH`, `TRIGGER_SOURCE`, `HUMAN_INSTRUCTION`, `FIX_ITERATION`, `REVIEW_BODY_FILE`, `PRE_AGENT_HEAD`, `FULLSEND_OUTPUT_SCHEMA`, `FULLSEND_OUTPUT_FILE`
- Keep at top level: `agent`, `doc`, `model`, `image`, `policy`, `role`, `slug`, `skills`, `host_files`, `timeout_minutes`, `validation_loop`

**`internal/scaffold/fullsend-repo/harness/retro.yaml`:**
- Move to `forge.github:`: `pre_script`, `post_script`
- Move to `forge.github.runner_env:`: `ORIGINATING_URL`, `REPO_FULL_NAME`, `GH_TOKEN`
- Keep at top level `runner_env:`: `FULLSEND_OUTPUT_SCHEMA`
- Keep at top level: `agent`, `doc`, `model`, `image`, `policy`, `role`, `slug`, `skills`, `host_files`, `timeout_minutes`, `validation_loop`

**`internal/scaffold/fullsend-repo/harness/prioritize.yaml`:**
- Move to `forge.github:`: `pre_script`, `post_script`
- Move to `forge.github.runner_env:`: `GITHUB_ISSUE_URL`, `GH_TOKEN`, `ORG`, `PROJECT_NUMBER`
- Keep at top level `runner_env:`: `FULLSEND_OUTPUT_SCHEMA`
- Keep at top level: `agent`, `doc`, `model`, `image`, `policy`, `role`, `slug`, `skills`, `host_files`, `timeout_minutes`

### Test updates

**`internal/scaffold/scaffold_test.go` -- `TestHarnessesLoadAndValidate`:**
- This test currently calls `harness.Load()` which does NOT resolve forge blocks. The `forge.github:` blocks will pass `validateForge()` (called from `Validate()`) but will not be merged into top-level fields.
- Update this test to also call `harness.LoadWithOpts(path, harness.LoadOpts{ForgePlatform: "github"})` on each template, verifying that forge resolution produces the expected merged state (e.g., `h.PreScript` is set, `h.RunnerEnv` contains the GitHub keys after merge).
- Add assertions: after `LoadWithOpts` with `github`, verify `h.Forge` is nil (consumed), `h.PreScript != ""`, `h.PostScript != ""`, and the merged `RunnerEnv` contains both top-level and forge-specific keys.

**After merge:** Scaffold harness templates are structurally portable. `fullsend run --forge github triage` produces identical behavior to the pre-PR state. `harness.Load()` (without forge) still works but leaves `pre_script` and `post_script` empty at the top level (they live inside `forge.github`). The runner always uses `LoadWithBase` which calls `ResolveForge`, so this is transparent at runtime.

---

## PR 3: Base URL generation and scaffold content hashing

**Scope:** Add infrastructure to generate `base:` URLs pointing to upstream scaffold harnesses, including content hashing for integrity verification. This is the foundation that PR 4 (install) uses to generate thin wrapper harnesses.

**Create `internal/scaffold/baseurl.go`:**

- `HarnessBaseURL(harnessName, commitSHA string) (string, error)`:
  - Returns `https://raw.githubusercontent.com/fullsend-ai/fullsend/<commitSHA>/internal/scaffold/fullsend-repo/harness/<harnessName>.yaml`
  - Validates `harnessName` matches `^[a-z][a-z0-9_-]*$` (same pattern as role validation)
  - Validates `commitSHA` is a 40-character hex string
  - No hash fragment -- the caller appends `#sha256=...` after computing the content hash

- `HarnessContentHash(harnessName string) (string, error)`:
  - Reads the embedded harness file from `fullsend-repo/harness/<harnessName>.yaml` via the embedded `embed.FS`
  - Returns the SHA-256 hex digest of the raw file content
  - This hash is the integrity hash that goes into the `#sha256=...` URL fragment
  - The hash is computed from the compile-time embedded content, which matches what `raw.githubusercontent.com` serves for the release tag's commit SHA

- `HarnessBaseURLWithHash(harnessName, commitSHA string) (string, error)`:
  - Convenience wrapper: calls `HarnessBaseURL` + `HarnessContentHash` and returns the full URL with `#sha256=...` fragment
  - Returns an error if the harness name does not exist in the embedded scaffold

- `HarnessNames() ([]string, error)`:
  - Returns the list of harness names available in the embedded scaffold (e.g., `["code", "fix", "prioritize", "retro", "review", "triage"]`)
  - Derived from `fullsend-repo/harness/*.yaml` in the embedded FS
  - Sorted alphabetically

**Design decisions:**

- **Commit SHA source:** The `commitSHA` parameter comes from the CLI's build-time version metadata. For tagged releases, GoReleaser sets the `version` variable via `-ldflags` which includes the git tag. The install flow can derive the commit SHA from the tag via `git ls-remote` or from a build-time constant. If the version is `"dev"` (local builds), the install flow should warn that base URLs cannot be generated (no stable commit SHA) and fall back to scaffolding full copies as today.

- **Content hash correctness:** The hash is computed from the embedded file content at the time the CLI binary was built. This matches what `raw.githubusercontent.com` serves for the exact commit the release was built from. If someone modifies the file after tagging but before building, the hash would be wrong -- GoReleaser's reproducible build pipeline prevents this.

- **The `allowed_remote_resources` prefix:** Generated base URLs use the `https://raw.githubusercontent.com/fullsend-ai/fullsend/` prefix. The install flow must add this prefix to `config.yaml`'s `allowed_remote_resources` if not already present (handled in PR 4).

**Create `internal/scaffold/baseurl_test.go`:**
- `HarnessBaseURL` returns expected URL format
- `HarnessBaseURL` rejects invalid harness names and commit SHAs
- `HarnessContentHash` returns a 64-character hex string for each known harness
- `HarnessContentHash` errors on unknown harness name
- `HarnessBaseURLWithHash` produces a valid URL with `#sha256=...` fragment
- `HarnessNames` returns the expected set of names, sorted
- Hash stability: hash of a known harness matches `sha256sum` of the embedded file content

**After merge:** The scaffold package can generate integrity-verified base URLs for any embedded harness template. No install flow changes yet.

---

## PR 4: Update `fullsend install` to generate thin wrapper harnesses

**Scope:** Change the admin install flow to generate thin harness wrappers with `base:` URLs instead of relying solely on the static embedded scaffold. Also write `role`/`slug` into the generated wrappers. The `config.yaml` `agents:` block continues to be written (dual-write for backward compatibility).

### Changes to `internal/layers/workflows.go`

The `WorkflowsLayer` currently uses `scaffold.WalkFullsendRepo()` which skips `layeredDirs` (including `harness/`). This is unchanged -- harness files are still NOT delivered by the workflows layer. Instead, a new layer handles harness wrapper generation.

### New layer: `internal/layers/harnesswrappers.go`

**`HarnessWrappersLayer` struct:**
- Fields: `org string`, `client forge.Client`, `printer *ui.Printer`, `agents []AgentCredentials`, `commitSHA string`, `existingHarnesses map[string]bool`
- `NewHarnessWrappersLayer(org string, client forge.Client, printer *ui.Printer, agents []AgentCredentials, commitSHA string) *HarnessWrappersLayer`

**`Install() error`:**
1. For each agent in `agents`:
   - Derive the harness name from `agent.Role`. Special case: role `"coder"` maps to harness name `"code"` (matching the existing scaffold convention where `code.yaml` has `role: coder`). Role `"fullsend"` is the org-level app and has no harness -- skip it.
   - Call `scaffold.HarnessBaseURLWithHash(harnessName, commitSHA)` to get the base URL
   - Generate wrapper YAML:
     ```yaml
     # This file is managed by fullsend. Do not edit it directly.
     # To customize, add overrides below the base: line.
     base: <base-url-with-hash>
     role: <agent.Role>
     slug: <agent.Slug>
     ```
   - For the `fix` role: the wrapper sets `role: coder` and `slug: <coder-slug>` because fix reuses the coder app (per `DefaultAgentRoles()` comment: "The fix stage reuses the coder app"). The fix harness name is `"fix"` but the role and slug come from the coder agent entry.
2. Check if the `.fullsend` config repo already has `harness/` directory with existing files (via `client.GetFile` or `client.ListDir`). If an existing harness file is present and NOT managed (no managed header), skip it -- the org has customized that harness and should not have it overwritten.
3. Commit all wrapper files via `client.CommitFiles()` in a single atomic commit.

**`Analyze() (*AnalysisResult, error)`:**
- Reports which wrapper files would be created/updated
- Flags existing non-managed harness files that would be skipped

**Fallback for dev builds:** When `commitSHA` is empty or `version` is `"dev"`, log a warning and skip wrapper generation. The existing scaffold delivery (via reusable workflow workspace) continues to work as-is. Wrapper generation is an enhancement for versioned releases only.

### Changes to `internal/cli/admin.go`

**`buildLayerStack()` (line 1860):**
- Add `HarnessWrappersLayer` to the layer stack, after `WorkflowsLayer` and before `SecretsLayer`
- Pass `commitSHA` derived from the CLI version:
  ```go
  commitSHA := resolveCommitSHA(version)
  ```
- Add `resolveCommitSHA(version string) string`:
  - If version matches a semver tag pattern (e.g., `v1.2.3`), extract the commit SHA from build metadata (a new build-time variable `var commitSHA = ""` set by GoReleaser's `-ldflags`)
  - If version is `"dev"`, return `""` (triggers fallback in HarnessWrappersLayer)

**`runInstall()` (line 1480):**
- No changes to the `config.yaml` writing path -- the `agents:` block continues to be written via `config.NewOrgConfig()`. This is the dual-write: both `config.yaml` and harness wrappers contain role/slug.

### Changes to `internal/cli/root.go`

- Add `var commitSHA = "dev"` alongside existing `var version = "dev"`, set via GoReleaser `-ldflags`
- Add `CommitSHA() string` accessor

### Changes to `internal/config/config.go`

**`OrgConfig` -- `AllowedRemoteResources`:**
- `NewOrgConfig()` should include `https://raw.githubusercontent.com/fullsend-ai/fullsend/` in `AllowedRemoteResources` by default, so generated base URLs pass the allowlist check without manual configuration. Only add this prefix if the org is using the default fullsend-ai scaffold (which it always is today).

### Harness name to role mapping

The scaffold uses harness filenames that don't always match roles:

| Harness file | `role:` value | Notes |
|---|---|---|
| `triage.yaml` | `triage` | Direct match |
| `code.yaml` | `coder` | Name differs from role |
| `review.yaml` | `review` | Direct match |
| `fix.yaml` | `coder` | Reuses coder app/slug |
| `retro.yaml` | `retro` | Direct match |
| `prioritize.yaml` | `prioritize` | Direct match |

The `HarnessWrappersLayer` maintains this mapping. A helper function `harnessNameForRole(role string) string` handles the `coder -> code` case. A separate `harnessesForRole(role string) []string` returns `["code", "fix"]` for role `"coder"` since both harnesses use the coder app.

### Test plan

**Create `internal/layers/harnesswrappers_test.go`:**
- Generates wrapper for each role -> valid YAML with `base:`, `role:`, `slug:`
- Coder role generates wrappers for both `code.yaml` and `fix.yaml`
- `fullsend` role (org app) -> skipped, no harness generated
- Dev build (`commitSHA=""`) -> no wrappers generated, warning logged
- Existing non-managed harness -> skipped with message
- Existing managed harness -> overwritten
- Generated wrapper loads successfully via `harness.LoadRaw()` and has expected Role/Slug
- Generated wrapper YAML is valid and parseable

**Modify `internal/cli/admin_test.go`:**
- Verify `HarnessWrappersLayer` is in the layer stack
- Verify `commitSHA` is passed through from CLI version metadata

**After merge:** `fullsend install` generates thin wrapper harnesses in the `.fullsend` config repo alongside the existing `config.yaml` agents block. Both locations contain role/slug. The wrappers reference upstream scaffold harnesses by URL with integrity hashes. Dev builds skip wrapper generation.

**Depends on:** PR 3 (base URL generation)

---

## PR 5: `fullsend lock --all` -- lock all harnesses in a directory

**Scope:** Today `fullsend lock` takes exactly one agent name. Generated wrappers all have `base:` URLs that need locking. Add `--all` flag to iterate all harness files and lock them in a single pass.

**Modify `internal/cli/lock.go`:**

- Change `cobra.ExactArgs(1)` to `cobra.RangeArgs(0, 1)` -- accept zero or one positional argument
- Add `--all` bool flag
- Validation:
  - `--all` and a positional argument are mutually exclusive -> error
  - Neither `--all` nor a positional argument -> error with usage hint
- When `--all` is set:
  1. Glob `filepath.Join(absFullsendDir, "harness", "*.yaml")` and `*.yml` to get all harness files. Uses `filepath.Glob` directly rather than `DiscoverAgents` to avoid coupling lock to the discover API -- lock only needs filenames, not role/slug.
  2. For each harness file found, run the existing lock logic (load, resolve, hash, record in lockfile). If any individual file fails, previously resolved harnesses are saved to the lock file (partial-progress semantics) so they don't need to be re-resolved on retry. The error message includes the failing harness name.
  3. Write the combined lock file once at the end with all harness entries.
  4. Report summary: `Locked N harnesses: triage, code, review, fix, retro, prioritize`

**Modify `internal/lock/lock.go`:**

- No schema changes needed. The lock file already supports multiple harness entries via `SetHarness(name, lock)`.
- Ensure `Save()` writes all harness entries atomically.

**Create `internal/cli/lock_all_test.go`:**
- `--all` with positional arg -> error
- `--all` with no harness files -> warning, empty lock file
- `--all` with multiple harnesses -> all locked, lock file contains entries for each
- `--all` with one harness having URL base, others local-only -> only URL-bearing harnesses get lock entries (or all get entries with empty deps -- follow existing convention)
- `--all` with one harness failing to parse -> error with harness name in message, partial progress saved for already-resolved harnesses

**After merge:** `fullsend lock --all` locks every harness in the directory. Combined with PR 4's wrapper generation, running `fullsend lock --all` after install pins all base URLs in `lock.yaml`.

---

## PR 6: Integration tests and end-to-end verification

**Scope:** End-to-end tests that verify the full Phase 2 flow: install generates wrappers, wrappers load and resolve correctly through the pipeline, lock pins base URLs, and the forge-restructured templates produce correct merged output.

**Create `internal/harness/phase2_integration_test.go`:**

1. **Wrapper -> LoadWithBase -> correct merge:**
   - Create a temp dir with a thin wrapper YAML (`base:` pointing to a local scaffold harness, `role: triage`, `slug: test-triage`)
   - Call `LoadWithBase()` with `ForgePlatform: "github"`
   - Verify the merged harness has:
     - `Role == "triage"`, `Slug == "test-triage"` (from wrapper, overriding base)
     - `Agent`, `Model`, `Image`, `Policy` inherited from base
     - `PreScript`, `PostScript` populated (from `forge.github:` after merge)
     - `RunnerEnv` contains both top-level keys (e.g., `FULLSEND_OUTPUT_SCHEMA`) and GitHub keys (e.g., `GH_TOKEN`) after forge resolution
     - `Skills` contains both base skills and forge skills (concatenated)
     - `Forge` is nil (consumed by ResolveForge)
     - `Base` is empty (consumed by LoadWithBase)

2. **All scaffold templates through forge.github resolution:**
   - For each harness in `scaffold.HarnessNames()`:
     - Load via `LoadWithOpts` with `ForgePlatform: "github"`
     - Verify `PreScript != ""` and `PostScript != ""` (they come from `forge.github`)
     - Verify `RunnerEnv` is non-empty and contains expected keys
     - Verify `Forge` is nil (consumed)
     - This catches any template where fields were incorrectly split between top-level and forge

3. **Backward compat: templates without forge flag:**
   - Load each scaffold template via `Load()` (no forge platform)
   - Verify it loads without error
   - Verify `PreScript` and `PostScript` are empty (they live in `forge.github:`, not top-level)
   - Verify `Forge` map is present and has `"github"` key
   - This confirms existing `Load()` callers are not broken

4. **DiscoverAgents on scaffold directory:**
   - Extract scaffold to temp dir, run `DiscoverAgents(harnessDir)`
   - Verify all 6 harnesses are discovered with correct role/slug pairs
   - Verify sorting by role

5. **Base URL integrity:**
   - For each harness, compute `HarnessContentHash(name)`
   - Load the embedded file directly from `embed.FS`, compute `sha256.Sum256`, compare
   - Verify `HarnessBaseURLWithHash` produces a URL whose hash fragment matches

**Update `internal/scaffold/scaffold_test.go`:**

- **`TestHarnessesLoadAndValidate`:** Add a parallel test path that loads each template with `LoadWithOpts(path, LoadOpts{ForgePlatform: "github"})` and verifies the merged state. Keep the existing `Load()` path for backward compat verification.

**After merge:** Full Phase 2 verification. All integration tests pass.

---

## Verification

After all PRs merge, verify Phase 2 end-to-end:

1. `make go-test` -- all new and existing tests pass
2. `make go-vet` -- no issues
3. `make lint` -- passes
4. **Scaffold templates:** Each template in `internal/scaffold/fullsend-repo/harness/` has `forge.github:` block with `pre_script`, `post_script`, and GitHub-specific `runner_env` keys. Platform-neutral `runner_env` keys remain at top level.
5. **Forge resolution:** `LoadWithOpts(path, {ForgePlatform: "github"})` on each scaffold template produces the same effective config as the pre-Phase-2 templates (same `PreScript`, `PostScript`, `RunnerEnv` key set). Verify with a comparison test.
6. **DiscoverAgents:** `DiscoverAgents(scaffoldHarnessDir)` returns 6 agents with correct role/slug pairs: `coder/fullsend-ai-coder` (code.yaml), `coder/fullsend-ai-coder` (fix.yaml), `prioritize/fullsend-ai-prioritize`, `retro/fullsend-ai-retro`, `review/fullsend-ai-review`, `triage/fullsend-ai-triage`.
7. **Base URLs:** `HarnessBaseURLWithHash("triage", "<sha>")` returns a well-formed URL with `#sha256=...` that matches the embedded file's hash.
8. **Wrapper generation:** Simulated install produces wrapper YAML files that parse correctly via `LoadRaw()` and contain expected `base:`, `role:`, `slug:` fields.
9. **Wrapper loading:** Wrapper YAML loaded via `LoadWithBase()` with `ForgePlatform: "github"` produces a fully-populated harness with all fields from the base scaffold resolved.
10. **Lock --all:** `fullsend lock --all` in a directory with wrapper harnesses records all base URL dependencies in `lock.yaml`.
11. **Dual write:** After install, both `config.yaml`'s `agents:` block and the harness wrapper files contain the same role/slug values for each agent.
12. **Backward compat:** Existing harnesses without `forge:` blocks or `base:` references load identically to pre-Phase-2 behavior via `Load()` and `LoadWithOpts()`.
