---
description: "Task list for implementing the top-level preflight_check harness field (ADR 0129, issue #8722)"
---

# Tasks: Top-Level Preflight Check

**Input**: Design documents from `/specs/001-top-level-preflight-check/`

**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/harness-preflight-check.md (loaded), quickstart.md (loaded)

**Tests**: INCLUDED — FR-011 and FR-012 explicitly require automated sentinel-action and composition tests, and quickstart.md §1 defines the assertions the suites must carry. Where a test pins behavior inherited from the reused nested-field machinery (research.md D2), it is labeled *characterization* (expected green once the US1 gate exists — it guards against drift); where it pins new behavior, it is labeled *red-first* (must FAIL until the story's implementation task lands).

**Organization**: Tasks grouped by user story (US1–US4 from spec.md, in priority order P1, P1, P2, P3) so each story is independently implementable and testable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks). Tasks sharing a file are sequential; the description names the task that precedes them in that file.
- **[Story]**: Which user story this task belongs to (US1–US4). Setup, Foundational, and Polish tasks carry no story label.
- Every task names its exact file path(s).

## Path Conventions

Go CLI repository (single module):

- Production code: `internal/harness/` (schema, composition, lint), `internal/cli/` (run gate, credential validation, commands)
- Tests: alongside production code (`*_test.go`), per repo convention
- Documentation: `docs/`
- Run tests with `go test ./...` from the repo root; invoke the CLI via `go run ./cmd/fullsend …` (AGENTS.md)

## Grounding (read before starting)

Every mechanism this feature needs already exists for the nested `validation_loop.preflight_check` (research.md "Grounding" table): the credential-safe executor `runPreflightCheck` in `internal/cli/preflight.go` (with a `source` label parameter), the allowlisted `preflightEnv` / `buildPreflightEnv`, pre-execution `${VAR}` rejection via `expandPreflightCommand`, `redactFeedback` redaction, the `preflightCheckTimeout` var (30s, overridable in tests), the `validateRunnerSecretRefs` refusal, the `preflightPathLike` lint guard, and the `preflightTestSetup` / `useFakeOpenshell` / `usePreScriptStub` helpers in `internal/cli/run_test.go`. The change wires one new field through these existing gates. Three files are deliberately UNCHANGED (research.md D1, D2, D10): `internal/cli/preflight.go` (shared executor — its `source` label already parameterizes it), `internal/harness/forge.go` (`ForgeConfig` deliberately gains no field), and `internal/agentnew/render.go` (the scaffold keeps the nested field; consumer migration is tracked in fullsend-ai/agents#1753, out of scope).

---

## Phase 1: Setup

**Purpose**: Confirm a clean baseline before any change.

- [x] T001 Verify the baseline on branch `001-top-level-preflight-check` from the repo root: `go build ./... && go vet ./... && go test ./...` all green (Go 1.26.5 toolchain per plan.md). If the baseline is red, STOP and report — do not start feature work on a broken tree.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The `Harness.PreflightCheck` struct member that every user story depends on.

**CRITICAL**: No user story work can begin until this phase is complete.

- [x] T002 Add the field to the `Harness` struct in `internal/harness/harness.go`, adjacent to `PreScript`/`PostScript` (research.md D1): `PreflightCheck string` with YAML tag `yaml:"preflight_check,omitempty"`. Do NOT add a field to `ForgeConfig` in `internal/harness/forge.go` — the field is harness-top-level only, and a `preflight_check:` key inside `forge:`/`overlays:` must remain silently ignored (the loader's non-strict `yaml.Unmarshal` gives this by omission, mirroring the `plugins`/`privilege_levels` precedent in docs/contributing/harness-fields.md). Add a GoDoc comment on the field stating the data-model.md constraints: optional string; literal host command (semantic type: inline command, ADR 0127/0128); default absent — no gate, status-quo behavior; no working directory set; never resource-resolved (passed verbatim to `sh -c`); the single eager host-dependency gate for `pre_script`, `post_script`, and `validation_loop` (ADR 0129).

- [x] T003 Add a YAML load test to `internal/harness/harness_test.go` (depends on T002): a harness document with a top-level `preflight_check: 'python3 -c "import jsonschema"'` unmarshals into `Harness.PreflightCheck` verbatim; a document without the key leaves the field as the empty string. No structural `Validate()` rule exists for this field (data-model.md) — load-only assertions.

**Checkpoint**: Foundation ready — the field exists and loads; user story implementation can begin.

---

## Phase 3: User Story 1 — Declare one early host-dependency gate (Priority: P1) MVP

**Goal**: A top-level `preflight_check` that runs exactly once per run, before `pre_script` and before sandbox creation, gating all later stages (FR-001, FR-002, FR-003).

**Independent Test** (spec.md US1): a harness whose top-level `preflight_check` command exits nonzero stops the run before sandbox creation with an error naming the failed check. Manually verifiable via quickstart.md §2.

### Tests for User Story 1 (red-first — must FAIL until T009 lands)

Extend the existing `TestRunAgent_PreflightCheck_*` family in `internal/cli/run_test.go` (this naming keeps the quickstart.md §1 `-run` patterns matching) and reuse the `preflightTestSetup` / `useFakeOpenshell` / `usePreScriptStub` helpers — the sandbox-creation stub fails loudly, so reaching a later error proves the earlier stage ran. All five tasks share `internal/cli/run_test.go`; write them sequentially.

- [x] T004 [US1] Ordering sentinel test (FR-011, US1-1) in `internal/cli/run_test.go`: a passing top-level check runs exactly once, before `pre_script` (when present) and before sandbox creation — assert sentinel-file write order across the check, the pre-script, and the sandbox stub, and that the check's sentinel action fired exactly once.

- [x] T005 [US1] Failure-path test (FR-003, US1-2) in `internal/cli/run_test.go` (same file as T004): a top-level check exiting nonzero returns an error containing `preflight_check failed` (field-qualified source label `preflight_check`; redacted command text and output per data-model.md Diagnostics), and no pre-script, sandbox, or nested `validation_loop.preflight_check` sentinel exists.

- [x] T006 [US1] Timeout test (FR-003, US1-3) in `internal/cli/run_test.go` (same file as T005): override the `preflightCheckTimeout` var to ~50ms; a non-terminating top-level command returns an error containing `preflight_check timed out`, with the same gating as a nonzero exit (no pre-script, no sandbox, no nested check).

- [x] T007 [US1] Skip-path test (FR-011, US1-5) in `internal/cli/run_test.go` (same file as T006): a pre-script that requests a run skip (exit 78) still observes the top-level check's sentinel — the eager gate runs before the skip decision (ADR 0129 trade-off: it checks post-script dependencies even on skip-eligible runs).

- [x] T008 [US1] Status-quo regression test (FR-001, US1-4, SC-002) in `internal/cli/run_test.go` (same file as T007): a harness with no top-level field — and one whose value is empty or whitespace-only ("absent, empty, or whitespace-only ⇒ treated as unset", data-model.md) — behaves exactly as before (no gate subprocess, no changed ordering); the pre-existing nested-field `TestRunAgent_PreflightCheck_*` tests pass unchanged.

### Implementation for User Story 1

- [x] T009 [US1] Insert the top-level run gate in `internal/cli/run.go` (research.md D2/D3/D4): immediately above the existing nested-check block (step "1d"), call the existing `runPreflightCheck(ctx, printer, "preflight_check", h.PreflightCheck, h.RunnerEnv, absFullsendDir)` guarded by `strings.TrimSpace(h.PreflightCheck) != ""`, passing the raw untrimmed value (only the emptiness decision trims — trimming would alter the author's command). A nonzero exit or timeout returns early, before `sandbox.EnsureAvailable`, `runPreScript`, `sandbox.CreateWithRetry`, and the nested check. Do NOT create a new executor or modify `internal/cli/preflight.go`: the `source` label parameter already field-qualifies diagnostics, and reusing the single executor keeps one reference gate (constitution principle I; ADR 0128 release requirement). The merged `h.RunnerEnv` (ADR 0055 `env.runner` merge) is already in effect at this insertion point — the nested call at the same location uses it today.

**Checkpoint**: User Story 1 is fully functional and independently testable — run the quickstart.md §2 manual scenario (a scratch harness with `preflight_check: 'test -x /nonexistent-tool'` stops the run with `preflight_check failed` before any sandbox activity; `'true'` lets the run proceed to later stages).

---

## Phase 4: User Story 2 — The gate is credential-safe and misuse-guarded (Priority: P1)

**Goal**: The top-level check runs under the same credential-safety gates and lint guardrails as the nested check — no token can leak into the command, its environment, or its diagnostics (FR-004, FR-005, FR-006; FR-007 path-like guard).

**Independent Test** (spec.md US2): a harness whose top-level check references a credential name or an out-of-allowlist variable is refused before any shell executes; a failing or timing-out check whose declared runner-env values are secrets reports only redacted command text and output.

### Tests for User Story 2

Red-first (must FAIL until T017/T018 land):

- [x] T010 [P] [US2] Runner-secret refusal test (FR-006, ADR 0136) in `internal/cli/runner_secrets_test.go`, extending `TestValidateRunnerSecretRefs`: a runner-secret name referenced in the top-level `preflight_check` is refused at harness validation — the error names the field `preflight_check` and the refused secret name(s) (data-model.md Diagnostics). RED until T017.

- [x] T011 [P] [US2] Path-like lint tests (FR-007, US2-4) in `internal/harness/lint_test.go`: a top-level value matching the bare script-path pattern (contract §7 regex `^[./]?[\w./-]+\.(sh|py|rb|js)$`, e.g. `scripts/common-preflight.sh`) emits a SeverityWarning diagnostic with Field `preflight_check` and the nested guard's message shape — "value looks like a script path; the check runs verbatim as `sh -c` and is never fetched — use `pre_script` for a fetched script or inline the command"; a literal command (e.g. `python3 -c "import jsonschema"`) emits no diagnostic; a whitespace-only value emits no diagnostic (the lint emptiness convention is `strings.TrimSpace`). RED until T018.

- [x] T012 [P] [US2] CLI lint surfacing test for the path-like warning in `internal/cli/lint_test.go`, extending `TestLintCommand`: `fullsend lint` prints the top-level path-like diagnostic for a harness whose `preflight_check` is path-like. RED until T018.

Characterization (pin the credential-safety behavior the top-level call site inherits from the reused executor — research.md D2; expected GREEN once T009 exists; they guard against drift):

- [x] T013 [P] [US2] Out-of-allowlist reference-rejection test (FR-005, US2-1) in `internal/cli/preflight_test.go`, mirroring the nested check's credential-safety tests for the top-level call site (source label `preflight_check`): a `${VAR}` brace reference to a name outside the preflight environment fails the run before any shell executes; the error names the variable (never a value) and points to the runner-env declaration channel. Cover the edge cases: bare `$VAR` (no braces) and non-plain forms (`${VAR:-default}`) are NOT validated pre-execution.

- [x] T014 [US2] Child-environment exclusion test (FR-004, US2-3) in `internal/cli/preflight_test.go` (same file as T013 — write after it): the check's child process sees exactly the host `PATH`, `HOME`, and `TMPDIR` (only when set), the runner-forced `FULLSEND_DIR` and `PYTHONDONTWRITEBYTECODE=1`, and non-credential variables declared via `env.runner` (or the deprecated `runner_env`); credential names — forge, mint, and provider tokens, runner secrets, and OIDC/trigger names — never reach the child, even when declared.

- [x] T015 [US2] Diagnostic-redaction test (FR-004, US2-2, SC-003) in `internal/cli/preflight_test.go` (same file as T014): a failing or timing-out top-level check whose declared runner-env values include a secret reports only redacted command text and output — the secret value never appears in failure or timeout diagnostics.

- [x] T016 [P] [US2] ValidateRunnerEnvWith exclusion test (research.md D5) in `internal/harness/harness_test.go`: the top-level field is NOT validated by `Harness.ValidateRunnerEnvWith` — a `${VAR}` declared only in `env.runner` must not fail host-env validation for this field (the preflight allowlist scan in `internal/cli` is the single reference gate; `ValidateRunnerEnvWith` runs before the ADR 0055 merge and cannot see harness declarations).

### Implementation for User Story 2

- [x] T017 [P] [US2] Add the runner-secret refusal in `internal/cli/runner_secrets.go` (research.md D6): extend `validateRunnerSecretRefs` to refuse runner-secret references in the top-level field alongside the existing `validation_loop.preflight_check` refusal (sketch: `errs.refuse("preflight_check", runnerSecretRefs(h.PreflightCheck, secrets))` — match the existing refusal's shape). No change to the harness package's `overlayRunnerSecretRefs` — the field cannot appear in an overlay/forge `ForgeConfig` (D1).

- [x] T018 [P] [US2] Add the path-like lint guard in `internal/harness/lint.go` (research.md D8a): a sibling of the nested path-like check for `h.PreflightCheck`, using the same `preflightPathLike` regex and message shape, Field `"preflight_check"`, SeverityWarning (non-fatal authoring diagnostic — ADR 0128 Decision 2 keeps it advisory in `fullsend lock`/`run`).

- [x] T019 [P] [US2] Extend the `ValidateRunnerEnvWith` doc comment in `internal/harness/harness.go` (research.md D5): state that both preflight fields (top-level `preflight_check` and nested `validation_loop.preflight_check`) are gated solely by the preflight allowlist scan in `internal/cli`; update the inline comment at the nested exclusion to say the same.

**Checkpoint**: User Stories 1 and 2 are both functional and independently testable — credential safety (spec US2 Independent Test) holds for the new field with no new credential surface.

---

## Phase 5: User Story 3 — Compose and override the gate (Priority: P2)

**Goal**: In `base:` composition the top-level `preflight_check` merges as a scalar — a child's non-empty value replaces the base's, never appends — and the field is not conditionally settable (FR-009, FR-012).

**Independent Test** (spec.md US3): compose a child over a base where both set `preflight_check`; the composed harness carries the child's command only. Compose a child that sets nothing and observe inheritance of the base's command.

### Tests for User Story 3

- [x] T020 [P] [US3] Scalar-override and inheritance tests (FR-009, US3-1/US3-2) in `internal/harness/compose_test.go`, asserting the data-model.md merge table verbatim: base unset + child unset = unset; base `A` + child unset = `A` (inherit); base `A` + child `B` = `B` (child overrides — never `A && B`, never concatenation); base unset + child `B` = `B`. RED on the inheritance case until T025 lands.

- [x] T021 [US3] Child-override-with-inherited-script-dependency test (FR-012 part 1) in `internal/harness/compose_test.go` (same file as T020): a base with `preflight_check: A` plus a `pre_script`, and a child that sets only `preflight_check: B` — the composed harness carries B as the single gate while the base's `pre_script` is inherited; the overriding author owns B covering the inherited script's dependencies (documented contract, not enforced — data-model.md Composition constraints).

- [x] T022 [US3] Overlay-selected-script-dependency test (FR-012 part 2) in `internal/harness/compose_test.go` (same file as T021): a harness whose overlays select different stage scripts per condition while `preflight_check` stays top-level — the single top-level check gates whichever scripts the overlays selected; the author must cover the union of dependencies across possible matching overlays. Assert the composed check value is unchanged by overlay selection.

- [x] T023 [US3] Ignored-key test (FR-009, US3-4) in `internal/harness/compose_test.go` (same file as T022): a `preflight_check:` key placed inside a `forge:` block or an `overlays:` entry is ignored — the loaded/composed harness does not carry that value; conditional values never replace the gate. Characterization: the non-strict loader already gives this by omission (research.md D1).

- [x] T024 [P] [US3] mergeForgeConfig non-settability test in `internal/harness/forge_test.go` (compose/forge merge-function invariant, docs/contributing/harness-composition.md): `ForgeConfig` has no preflight field and `mergeForgeConfig` cannot set or change `Harness.PreflightCheck` — the invariant is preserved by omission because the field is absent from both functions (research.md D7 rationale). Characterization/compile-level guard.

### Implementation for User Story 3

- [x] T025 [US3] Add the scalar merge in `internal/harness/compose.go` `mergeBaseIntoChild` (research.md D7): `if child.PreflightCheck == "" { child.PreflightCheck = base.PreflightCheck }` in the scalar block next to the `PreScript`/`PostScript` pattern. Do NOT touch `mergeForgeConfig` in `internal/harness/forge.go`, and do NOT append or combine base and child commands (ADR 0129 Decision 4 — concatenated probes would be order- and shell-fragile).

**Checkpoint**: User Story 3 independently testable — every composed-harness scenario yields exactly one effective gate value (SC-004).

---

## Phase 6: User Story 4 — Migrate away from the nested field (Priority: P3)

**Goal**: The nested `validation_loop.preflight_check` keeps working, tells its author it is deprecated, and runs after the top-level check when both are configured (FR-007 deprecation warning, FR-008).

**Independent Test** (spec.md US4): a harness with only a nested `validation_loop.preflight_check` runs exactly as before, plus a deprecation warning naming the replacement; a harness with both fields runs the top-level check first and then the nested one.

### Tests for User Story 4

- [x] T026 [P] [US4] Both-fields ordering test (FR-008, US4-2) in `internal/cli/run_test.go` (extends the US1 family; write after T004–T008): a harness setting both the top-level `preflight_check` and `validation_loop.preflight_check` — when both pass, the top-level sentinel precedes the nested sentinel; when the top-level check fails, the nested sentinel is absent. The pre-existing nested-only failure tests already pin "top-level passes, nested fails → stop at the nested check, status quo" — keep them green.

- [x] T027 [P] [US4] Nested-field deprecation lint test (FR-007, US4-1/US4-3) in `internal/harness/lint_test.go` (same file as US2's T011/T018 work — sequence after it): a harness that sets `validation_loop.preflight_check` emits a SeverityWarning deprecation diagnostic on Field `validation_loop.preflight_check` naming the top-level `preflight_check` replacement; a harness without the nested field emits no deprecation diagnostic. RED until T029.

- [x] T028 [P] [US4] CLI lint surfacing test for the deprecation warning in `internal/cli/lint_test.go` (same file as T012 — sequence after it), extending `TestLintCommand`: `fullsend lint` prints the nested-field deprecation warning for a harness that sets `validation_loop.preflight_check`. RED until T029.

### Implementation for User Story 4

- [x] T029 [US4] Add the nested-field deprecation warning in `internal/harness/lint.go` (research.md D8b): when `h.ValidationLoop.PreflightCheck` is set (non-empty after trim, matching the lint guard's emptiness convention), emit a SeverityWarning deprecation diagnostic naming the top-level `preflight_check` replacement field. The warning ships with this change because the replacement becomes usable when this change ships (spec.md Assumption 3); it is non-fatal, and removal of the nested field is out of scope (ADR 0127 version gate).

**Checkpoint**: All four user stories are independently functional; the migration signal is in place (SC-005).

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Documentation (FR-010, research.md D9) and full verification. Follow the repo's documentation conventions (docs/contributing/documentation.md, including the line-wrapping rule); no `docs/.vitepress/config.ts` sidebar change is needed — no new pages (research.md D9).

- [x] T030 [P] Update `docs/contributing/harness-fields.md` (research.md D9.1): in the "Fields that stay at top level only" table, drop the "Planned, not yet implemented" marker from the `preflight_check` row and state "Top level only — not a `ForgeConfig` field" (matching the `plugins`/`privilege_levels` rows); in the semantic-type table, move top-level `preflight_check` from "(planned)" to implemented alongside the nested field; in the merge-rules section, document the scalar merge and top-level-only settability.

- [x] T031 [P] Update `docs/reference/harness-reference.md` (research.md D9.2): add a new top-level `preflight_check` field section — literal `sh -c` command semantics (never fetched, no working directory set), the allowlisted preflight environment, ordering (exactly once, before `pre_script` and sandbox creation), scalar composition, and the nested-field migration/deprecation note.

- [x] T032 [P] Update `docs/cli/lint.md` (research.md D9.3): add two diagnostics rows — the top-level path-like authoring warning (Field `preflight_check`) and the nested-field deprecation warning (Field `validation_loop.preflight_check`).

- [x] T033 [P] Update `docs/architecture.md` (research.md D9.4): update the ADR 0129 passage (around lines 243–250) from "planned but not yet implemented" to current state; refresh the stale adjacent ADR 0128 passage that still calls the already-landed nested path-like lint guard "planned".

- [x] T034 [P] Review `docs/guides/user/customizing-agents.md` (research.md D9.5) for a cross-reference from the `validation_loop.preflight_check` passage to the new top-level field (AGENTS.md doc-reference rule); check `grep -rn 'preflight_check' docs/guides/` for other passages that substantively discuss the check and cross-reference where warranted.

- [x] T035 Run full verification from the repo root per quickstart.md: the focused suites (`go test ./internal/cli/ -run 'TestRunAgent_PreflightCheck|TestRunPreflightCheck|TestValidateRunnerSecretRefs' -v`; `go test ./internal/cli/ -run 'TestLintCommand' -v`; `go test ./internal/harness/ -run 'PreflightCheck|Lint' -v`), then `go build ./... && go vet ./...` and `go test ./...` (all green, including the untouched pre-existing nested-field tests), then the manual end-to-end scenario of quickstart.md §2 (failing check stops the run with `preflight_check failed`; `'true'` proceeds; lint shows the path-like and deprecation warnings).

- [x] T036 Verify the Go patch-coverage gate before committing (constitution: 80% target, 75% enforced floor): follow `.agents/skills/check-patch-coverage/SKILL.md` (approximate-local-coverage flow) over the production changes in `internal/harness/` and `internal/cli/`; close gaps by adding tests — never by weakening the gate. If the floor is genuinely unreachable, STOP and report the conflict (gatekeeping integrity).

---

## Dependencies & Execution Order

### Phase Dependencies

```text
Setup ──► Foundational ──┬──► US1 (P1) ──► US2 (P1) ──► US4 (P3) ──┐
(T001)    (T002–T003)    │                                            ├──► Polish
                         └──► US3 (P2) ──────────────────────────────┘    (T030–T036)
```

- **Setup (Phase 1)**: no dependencies — start immediately.
- **Foundational (Phase 2)**: depends on Setup; BLOCKS all user stories (the struct member must exist first).
- **US1 (Phase 3)**: depends on Phase 2 only.
- **US2 (Phase 4)**: its validation/lint tasks need only Phase 2, but its run-path characterization tests (T013–T015) exercise the gate from T009 — keep the US1 → US2 order.
- **US3 (Phase 5)**: depends on Phase 2 only; its files (`internal/harness/compose.go`, `compose_test.go`, `forge_test.go`) are disjoint from US1's (`internal/cli/run.go`, `run_test.go`) — US3 CAN proceed in parallel with US1 if separately staffed.
- **US4 (Phase 6)**: T026 needs US1's gate; T027–T029 share `internal/harness/lint.go` and `lint_test.go` with US2 (T011, T018) and `internal/cli/lint_test.go` with US2 (T012) — sequence US4 after US2.
- **Polish (Phase 7)**: after all desired user stories complete; T035/T036 are the final gates before committing.

### User Story Dependencies

- **US1 (P1)**: after Phase 2. No dependencies on other stories.
- **US2 (P1)**: after US1 (run-path tests) — mostly reuses machinery that already exists (research.md D2).
- **US3 (P2)**: after Phase 2 only; independent of US1/US2/US4 (file-disjoint).
- **US4 (P3)**: after US1 (gate) and US2 (shared lint files); its deprecation warning makes sense only once the replacement field ships, which is this change.

### Within Each User Story

- Red-first tests are written and observed to FAIL before the story's implementation task.
- Characterization tests may be written any time after their prerequisite exists (T009 for T013–T015).
- Same-file tasks run sequentially — each description names its file predecessor.
- US2 implementation tasks T017/T018/T019 are mutually file-disjoint and may run in parallel.

### Parallel Opportunities

- Phase 2 → {US1 ∥ US3} (disjoint files) — the widest safe parallel split.
- US2 tests in distinct files (T010, T011, T012, T013, T016) can run together; T014 and T015 then follow T013 in `preflight_test.go`.
- US2 implementation tasks T017, T018, T019 can run together.
- US3: T020 (compose_test.go) ∥ T024 (forge_test.go), then T021–T023 follow T020 in `compose_test.go`.
- US4: T026 (run_test.go) ∥ T027 (harness/lint_test.go) ∥ T028 (cli/lint_test.go).
- Polish: T030–T034 are five distinct doc files — all parallel.

---

## Parallel Example: User Story 2

```bash
# Launch the file-disjoint red-first tests together:
Task: "Runner-secret refusal test in internal/cli/runner_secrets_test.go"            # T010
Task: "Path-like lint tests in internal/harness/lint_test.go"                         # T011
Task: "CLI lint surfacing test in internal/cli/lint_test.go"                          # T012

# Launch the file-disjoint characterization tests alongside them:
Task: "Out-of-allowlist reference-rejection test in internal/cli/preflight_test.go"   # T013
Task: "ValidateRunnerEnvWith exclusion test in internal/harness/harness_test.go"      # T016

# Then sequentially in preflight_test.go: T014 (child-env exclusion), T015 (redaction)

# Launch the file-disjoint implementation tasks together:
Task: "Runner-secret refusal in internal/cli/runner_secrets.go"                       # T017
Task: "Path-like lint guard in internal/harness/lint.go"                              # T018
Task: "ValidateRunnerEnvWith doc comment in internal/harness/harness.go"              # T019
```

## Parallel Example: User Story 3

```bash
# Launch the file-disjoint tests together:
Task: "Scalar-override and inheritance tests in internal/harness/compose_test.go"     # T020
Task: "mergeForgeConfig non-settability test in internal/harness/forge_test.go"       # T024

# Then sequentially in compose_test.go: T021, T022, T023
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 (baseline) and Phase 2 (the field).
2. Complete Phase 3 (US1): red tests → gate → green.
3. **STOP and VALIDATE**: run the quickstart.md §2 manual scenario — the eager host-dependency gate works standalone.
4. Note: US1 alone is demoable, but do NOT open the PR yet — US2 is a co-required P1 release gate (ADR 0128/0129 and issue #8721: credential safety must hold for both checks before the top-level field is released).

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. + US1 → validate independently (MVP).
3. + US2 → credential safety holds; **shippable minimum** (both P1 stories complete).
4. + US3 → composition semantics proven (SC-004).
5. + US4 → migration signal in place (SC-005).
6. + Polish → documentation current (FR-010), full suite green, patch coverage ≥ 80% target (75% floor).

### Parallel Team Strategy

1. Team completes Setup + Foundational together.
2. Once Foundational is done: Developer A → US1, Developer B → US3 (file-disjoint).
3. After US1: US2; after US2: US4 (or one developer continues in priority order).
4. Polish is parallelizable across the team (five distinct doc files), with one owner for T035/T036.

---

## Notes

- [P] tasks = different files, no dependencies; same-file tasks are sequential and say so.
- [Story] labels map tasks to spec.md user stories for traceability; FR/scenario numbers cite spec.md; D-numbers cite research.md decisions.
- Deliberately UNCHANGED files (do not "fix" them): `internal/cli/preflight.go` (D2 — shared executor), `internal/harness/forge.go` (D1/D7 — no ForgeConfig field), `internal/agentnew/render.go` (D10 — scaffold keeps the nested field; consumer migration tracked in fullsend-ai/agents#1753).
- Schema version stays `1` (ADR 0127): an older pinned consumer CLI silently ignores the new key; no version-bump task exists by design.
- Nested-field removal, a resource-resolved `preflight_script` variant, and the consumer pin-verification rollout are out of scope (spec.md Assumptions).
- Commit conventions: Conventional Commits per COMMITS.md (the type prefix feeds release notes); this human-directed checkout also uses `git commit -s` plus an `AI-Assisted: true` trailer (local `.opencode/AGENTS.md`).
- Stage changes before running `make lint` (pre-commit only checks staged files).
- Stop at any checkpoint to validate a story independently; commit after each task or logical group.
