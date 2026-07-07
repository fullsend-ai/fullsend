# Implementation Plan: Deprecate customized/ directory overlay (ADR-0064)

## Conventions

Line numbers in this plan are pinned to the codebase at the time of
writing and will drift as PRs merge. Function and symbol names are
the primary references; line numbers are supplementary aids for initial
orientation. When implementing a PR, use the function/symbol name to
locate the current position rather than relying on line numbers.

## Context

ADR-0064 deprecates the `customized/` directory overlay mechanism
introduced by [ADR 0035](../ADRs/0035-layered-content-resolution.md).
The overlay is superseded by `base:` harness composition
([ADR 0045](../ADRs/0045-forge-portable-harness-schema.md)), URL-based
resource references ([ADR 0038](../ADRs/0038-universal-harness-access.md)),
and config-based agent registration
([ADR 0058](../ADRs/0058-agent-registration.md)).

The `customized/` overlay touches these subsystems:

| Subsystem | Key files | Surface |
|-----------|-----------|---------|
| Reusable workflows | `.github/workflows/reusable-{triage,code,review,fix,retro,prioritize}.yml` | `CUSTOM_BASE` overlay loop in workspace-prepare step |
| Scaffold workflow | `internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml` | Hardcoded `customized/scripts` overlay (not via `CUSTOM_BASE`) |
| Scaffold (Go) | `internal/scaffold/scaffold.go` | `layeredDirs`, `CustomizedDirs()`, `PerRepoCustomizedDirs()`, `isSkippedDir()`, `IsLayeredPath()` |
| Scaffold (install) | `internal/scaffold/installfiles.go` | `customizedDirsForPrefix()`, `.gitkeep` generation |
| Scaffold (vendor) | `internal/scaffold/vendorcontent.go` | `IsLayeredPath()` call |
| Scaffold (embed) | `internal/scaffold/fullsend-repo/customized/` | 8 empty subdirectories with `.gitkeep` files |
| Scaffold (data) | `internal/scaffold/fullsend-repo/scripts/.pre-commit-tools.yaml` | Comments documenting `customized/scripts/` as L1 override path |
| Harness wrappers | `internal/layers/harnesswrappers.go` | `wrapperHeader` comment referencing `customized/harness/` |
| User docs | `docs/guides/user/customizing-agents.md` | Guide pointing users to `customized/` |
| Agent docs | `docs/agents/README.md`, `docs/agents/{triage,review}.md` | References to `customized/` |
| Architecture docs | `docs/architecture.md`, `docs/runtimes.md` | References to layered content resolution |
| Other guides | `docs/guides/user/running-agents-locally.md`, `docs/guides/user/customizing-with-agents-md.md`, `docs/guides/user/customizing-with-skills.md`, `docs/guides/user/building-custom-agents.md`, `docs/guides/dev/cli-internals.md` | References to `customized/` |
| ADR cross-refs | `docs/ADRs/0033-*.md`, `0043-*.md`, `0044-*.md`, `0047-*.md`, `0053-*.md`, `0056-*.md`, `0059-*.md` | References to `customized/` directories or ADR 0035 |
| Other plans | `docs/plans/deprecate-per-org-install.md` | References to `customized/` copy step |

## Prerequisites

This work should begin once the following are fully implemented and in
production:

- ADR 0045 (forge-portable harness schema) — all phases complete,
  `base:` composition working in production
- ADR 0038 (universal harness access) — URL-based resource references
  working in production
- ADR 0058 (agent registration) — config-based agent discovery working
  in production

## PR Dependency Graph

```
PR 1 (deprecation warnings) ──> PR 2 (remove overlay from workflows)
                                  │
PR 3 (remove scaffold code) ─────┤
                                  │
PR 4 (update docs) ──────────────┤
                                  │
                                  └──> PR 5 (final cleanup + grep sweep)
```

PRs 2, 3, and 4 can be developed in parallel after PR 1 merges. PR 5
is the final sweep after all prior PRs merge.

---

## PR 1: Add deprecation warnings

**Scope:** Emit warnings when `customized/` directories contain real
files. Zero behavioral change — the overlay still runs.

**`internal/scaffold/installfiles.go`:**

- In `CollectInstallFiles()` (~line 42) and `CollectPerRepoInstallFiles()`
  (~line 79), continue generating `.gitkeep` files but add a comment
  noting they will be removed in a future release.

**Reusable workflows (all 6):**

- After the `CUSTOM_BASE` overlay loop, add a step that checks whether
  any non-`.gitkeep` files were copied. If so, emit a warning:
  ```
  ::warning::Files in ${CUSTOM_BASE}/ are deprecated. Migrate to base: harness composition (ADR-0045). See docs/guides/user/customizing-agents.md
  ```

**After merge:** Users with files in `customized/` see warnings in CI
logs. No behavioral change.

---

## PR 2: Remove overlay loop from reusable workflows

**Scope:** Remove the `CUSTOM_BASE` overlay from all 6 reusable
workflows.

**`.github/workflows/reusable-triage.yml` (~lines 100–113):**
**`.github/workflows/reusable-code.yml` (~lines 102–113):**
**`.github/workflows/reusable-review.yml` (~lines 100–113):**
**`.github/workflows/reusable-fix.yml` (~lines 118–127):**
**`.github/workflows/reusable-retro.yml` (~lines 99–113):**
**`.github/workflows/reusable-prioritize.yml` (~lines 103–113):**

In each workflow's "Prepare workspace" step, remove the block:

```yaml
CUSTOM_BASE="customized"
if [[ "${INSTALL_MODE}" == "per-repo" ]]; then
  CUSTOM_BASE=".fullsend/customized"
fi
for dir in ${LAYERED_DIRS}; do
  if [[ -d "${CUSTOM_BASE}/${dir}" ]]; then
    find "${CUSTOM_BASE}/${dir}" -type f ! -name '.gitkeep' -print0 \
      | while IFS= read -r -d '' f; do
            rel="${f#"${CUSTOM_BASE}"/}"
            mkdir -p "$(dirname "${rel}")"
            cp "${f}" "${rel}"
          done
  fi
done
```

The upstream default copy loop (`for dir in ${LAYERED_DIRS}; do ... cp -r
"${SRC}/${dir}/." "${dir}/" ... done`) remains — it still populates the
workspace with upstream content. Only the customized overlay on top is
removed.

**`internal/scaffold/fullsend-repo/.github/workflows/repo-maintenance.yml`
(~lines 57–63):**

This workflow has its own hardcoded `customized/scripts` overlay that
does NOT use the `CUSTOM_BASE` variable. Remove the block:

```yaml
if [[ -d "customized/scripts" ]]; then
  find "customized/scripts" -type f ! -name '.gitkeep' -print0 \
    | while IFS= read -r -d '' f; do
          rel="${f#customized/}"
          ...
      done
fi
```

**After merge:** Reusable workflows no longer overlay `customized/`
files. Any files users placed there are silently ignored (they should
have migrated after PR 1 warnings).

---

## PR 3: Remove scaffold code for customized directories

**Scope:** Remove Go code that generates and manages `customized/`
directories.

**Delete embedded directories:**

- `internal/scaffold/fullsend-repo/customized/` — the entire directory
  tree (8 subdirectories, 8 `.gitkeep` files).

**`internal/scaffold/scaffold.go`:**

- Remove `CustomizedDirs()` (~line 117–125).
- Remove `PerRepoCustomizedDirs()` (~line 127–135).
- Update comment on `layeredDirs` (~line 57–59) to remove reference to
  `customized/<dir>/` and ADR 0035; reference ADR 0064 instead.

**`internal/scaffold/installfiles.go`:**

- Remove the `.gitkeep` generation loop in `CollectInstallFiles()`
  (~lines 42–48) that calls `customizedDirsForPrefix()`.
- Remove the `.gitkeep` generation loop in `CollectPerRepoInstallFiles()`
  (~lines 77–84) that calls `PerRepoCustomizedDirs()`.
- Remove `customizedDirsForPrefix()` (~lines 53–58).

**`internal/layers/harnesswrappers.go`:**

- Update `wrapperHeader` (~line 13) to remove the reference to
  `customized/harness/` and ADR-0035. Replace with guidance pointing to
  `base:` composition (ADR-0045).

**`internal/scaffold/fullsend-repo/scripts/.pre-commit-tools.yaml`
(~lines 37–39):**

- Remove comments documenting `customized/scripts/` as the L1 override
  path. Update to reference L2 additive merge at repo root instead.

**Test updates:**

- `internal/scaffold/scaffold_test.go`: Remove assertions for
  `CustomizedDirs()`, `PerRepoCustomizedDirs()`, and any tests that
  verify `customized/` directory contents (including `scaffold_test.go`
  assertion for `repo-maintenance.yml` overlay at ~line 735).
- `internal/scaffold/installfiles_test.go`: Remove assertions for
  `.gitkeep` files in `customized/` paths.
- `internal/layers/harnesswrappers_test.go`: Update `wrapperHeader`
  assertions if any.
- `internal/layers/workflows_test.go`: Remove assertion for
  `customized/agents/.gitkeep` file mode (~line 455).
- `e2e/admin/admin_test.go`: Remove assertions for all 8
  `customized/*.gitkeep` files (~lines 173–180).

**After merge:** `fullsend admin install` no longer creates `customized/`
directories. The scaffold embed no longer contains them.

---

## PR 4: Update documentation

**Scope:** Documentation-only. No code changes.

**`docs/guides/user/customizing-agents.md`:**

- Rewrite to present `base:` harness composition (ADR 0045) as the
  primary customization mechanism.
- Remove all references to `customized/` directories.
- Add examples of thin harness wrappers with `base:` URLs.
- Add migration guidance for users who had files in `customized/`.

**`docs/agents/triage.md`, `docs/agents/review.md`:**

- Remove references to `customized/` if present.

**`docs/architecture.md`:**

- Update layered content resolution description to reflect that
  `customized/` is removed.
- Reference ADR 0064 as superseding ADR 0035 for customization.

**`docs/runtimes.md`:**

- Update workspace preparation description to remove the overlay step.

**`docs/guides/user/running-agents-locally.md`:**
**`docs/guides/user/customizing-with-agents-md.md`:**
**`docs/guides/user/customizing-with-skills.md`:**
**`docs/guides/user/building-custom-agents.md`:**
**`docs/guides/dev/cli-internals.md`:**

- Remove or update references to `customized/` directories.

**`docs/ADRs/0035-layered-content-resolution.md`:**

- Add a note at the top of the Status section: "Superseded by
  [ADR 0064](../ADRs/0064-deprecate-customized-directory-overlay.md)."
- Do not rewrite the body — ADRs are point-in-time records.

**Other ADRs with cross-references:**

- `0033-per-repo-installation-mode.md`
- `0043-managed-file-headers.md`
- `0044-deprecate-per-org-installation-mode.md`
- `0047-vendored-installs-with-vendor-flag.md`
- `0053-agent-driven-branch-targeting.md`
- `0056-per-repo-precommit-tools-registry.md`
- `0059-public-mint-mode-with-wildcard-allowlists.md`

For each, add a brief annotation noting that `customized/` references
are superseded by ADR 0064. Do not rewrite ADR bodies.

Note: ADRs 0036 and 0055 use "customized" only as an English word, not
as a path reference — they do not need updates.

**`docs/plans/deprecate-per-org-install.md` (~lines 212–216):**

- Add a supersession annotation noting that the `customized/` copy step
  described in the migration command (PR 4) is superseded by ADR 0064.

**`docs/plans/adr-0045-forge-portable-harness-phase2.md`:**

- Update references to `customized/` if present.

**After merge:** All documentation points to `base:` composition as the
customization mechanism.

---

## PR 5: Final cleanup and grep sweep

**Scope:** Final sweep. Depends on all prior PRs.

**Grep sweep:**

Run a final grep for stale references:

```bash
grep -rn 'customized/' --include='*.go' --include='*.yml' --include='*.yaml' --include='*.md' --include='*.ts' --include='*.tsx' \
  | grep -v '.git/' | grep -v 'node_modules/' | grep -v 'CHANGELOG'
grep -rn 'CustomizedDirs\|PerRepoCustomizedDirs\|customizedDirsForPrefix' --include='*.go'
grep -rn 'CUSTOM_BASE' --include='*.yml' --include='*.yaml'
grep -rn 'ADR.0035\|ADR-0035\|adr.0035' --include='*.go' --include='*.yml' --include='*.yaml'
```

Fix or remove any remaining references. The `sentencetoken/token.go`
reference to "customized" is unrelated (it refers to prose
customization) and should be left alone.

**After merge:** No stale references to `customized/` remain in the
codebase.
