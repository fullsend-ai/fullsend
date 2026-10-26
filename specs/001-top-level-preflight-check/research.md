# Research: Top-Level Preflight Check

**Feature**: [spec.md](spec.md) | **Branch**: `001-top-level-preflight-check` | **Date**: 2026-10-26

Phase 0 research resolved every technical unknown through direct codebase
exploration, the accepted ADRs (0127, 0128, 0129), and Dewey prior learnings
(`learning/harness-fields-20261023T121014-jay-flowers`,
`learning/preflight-check-20261022T201221-jay-flowers`,
`learning/harness-expansion-20261022T201218-jay-flowers`). The spec carried no
NEEDS CLARIFICATION markers (see
[checklists/requirements.md](checklists/requirements.md)); the research below
grounds each spec requirement in the current code so Phase 1 design and Phase 2
tasks reference real anchors.

## Grounding: what already exists (verified in code)

| Mechanism | Location | Status |
|---|---|---|
| Nested field | `internal/harness/harness.go` — `ValidationLoop.PreflightCheck string` (`yaml:"preflight_check,omitempty"`) | Landed (#5192) |
| Credential-safe executor | `internal/cli/preflight.go` — `runPreflightCheck(ctx, printer, source, rawCmd, runnerEnv, fullsendDir)` with `source` label parameter | Landed (#8721) |
| Allowlisted environment | `internal/cli/preflight.go` — `preflightEnv` / `buildPreflightEnv` (host `PATH`/`HOME`/`TMPDIR`, runner-forced `FULLSEND_DIR`/`PYTHONDONTWRITEBYTECODE`, non-credential `env.runner` declarations; minted-token inventory + `harnessExpansionDenied` + GitLab role-prefix refusal) | Landed (#8721, #8748) |
| Pre-execution `${VAR}` rejection | `internal/cli/preflight.go` — `expandPreflightCommand` (names the variable, never a value; points to the declaration channel) | Landed |
| Redaction on failure/timeout | `internal/cli/preflight.go` — `redactFeedback` over command text and output | Landed |
| Execution bound | `internal/cli/run.go` — `preflightCheckTimeout` (30s; overridable in tests) | Landed |
| Nested gate call site | `internal/cli/run.go` step "1d" — before `sandbox.EnsureAvailable` (1432), `runPreScript` (1908), `sandbox.CreateWithRetry` (1975) | Landed |
| Runner-secret refusal | `internal/cli/runner_secrets.go` — `validateRunnerSecretRefs` refuses `validation_loop.preflight_check` refs | Landed (ADR 0136) |
| Path-like lint guard | `internal/harness/lint.go` — `preflightPathLike` regex, SeverityWarning, Field `validation_loop.preflight_check` | Landed (#7600) |
| Env-validation exclusion | `internal/harness/harness.go` — `ValidateRunnerEnvWith` deliberately skips the nested field (single reference gate is the allowlist scan) | Landed |
| Scalar compose merge precedent | `internal/harness/compose.go` — `mergeBaseIntoChild` scalar block (PreScript/PostScript pattern) | Landed |
| Forge/overlay merge | `internal/harness/forge.go` — `mergeForgeConfig` (ForgeConfig has no preflight field) | Landed |
| Test scaffolding | `internal/cli/run_test.go` — `preflightTestSetup`, `useFakeOpenshell`, `usePreScriptStub` (sandbox-creation stub fails loudly, proving return-before-sandbox), sentinel-file patterns | Landed |

## Decisions

### D1 — Field declaration: `Harness.PreflightCheck string`, top level only

- **Decision**: Add `PreflightCheck string \`yaml:"preflight_check,omitempty"\`` to
  the `Harness` struct in `internal/harness/harness.go`, adjacent to
  `PreScript`/`PostScript`. Do **not** add a field to `ForgeConfig`.
- **Rationale**: FR-001/FR-009 and ADR 0129 make the field top-level only. A
  `preflight_check:` key inside a `forge:` block or `overlays:` entry maps to no
  `ForgeConfig` field, and the loader uses non-strict `yaml.Unmarshal`
  (`parseRaw`, `parseHarnessContent`), so the key is silently ignored — exactly the
  FR-009 behavior, achieved by omission. This mirrors the existing precedent for
  `plugins` and `privilege_levels` ("Top level only — not a `ForgeConfig` field",
  harness-fields.md).
- **Alternatives considered**: (a) Add the field to `ForgeConfig` and merge it in
  `mergeForgeConfig` — rejected: FR-009 forbids conditional setters, and a merged
  forge value could replace the gate. (b) Reject unknown keys under `forge:`/
  `overlays:` loudly — rejected: changes loader behavior for *all* unknown keys,
  far beyond this feature's scope and contrary to the documented ignore semantics.

### D2 — Execution: reuse `runPreflightCheck` with source label `"preflight_check"`

- **Decision**: The run gate calls the existing
  `runPreflightCheck(ctx, printer, "preflight_check", h.PreflightCheck, h.RunnerEnv, absFullsendDir)`.
  No new execution helper.
- **Rationale**: The executor was built with a `source` label parameter precisely
  so error messages stay field-qualified for multiple call sites. Reusing it gives
  FR-002/FR-003/FR-004/FR-005 their entire credential-safety and redaction
  behavior for free and keeps a single reference gate (Constitution principle I;
  ADR 0128's release requirement). The merged `h.RunnerEnv` (ADR 0055
  `env.runner` merge) is already in effect at the insertion point — the nested
  call at the same location uses it today.
- **Alternatives considered**: A dedicated top-level executor with its own
  timeout/redaction — rejected: duplicates the single gate, risks drift between
  the two checks, and violates DRY with zero capability gained.

### D3 — Insertion point: immediately before the nested check in `run.go`

- **Decision**: Insert the top-level gate as a new step directly above the
  existing "1d" nested-check block in `internal/cli/run.go`.
- **Rationale**: Verified ordering at that location: it precedes
  `sandbox.EnsureAvailable` (host availability probe), `runPreScript`, and
  `sandbox.CreateWithRetry` — satisfying FR-002 ("before `pre_script` and before
  sandbox creation") and ADR 0129's ordering. Placing it before the nested check
  satisfies FR-008/US4 ("top-level check runs first; nested runs only if it
  passed") because a failing top-level check returns early.
- **Alternatives considered**: (a) After `sandbox.EnsureAvailable` — rejected:
  unnecessary host-probe coupling; the nested precedent sits before it, and
  "before sandbox creation" is most conservative. (b) Before harness validation —
  rejected: the gate needs the composed, forge/overlay-resolved harness
  (`h.RunnerEnv` post-merge), which only exists after load/compose completes.

### D4 — Empty and whitespace-only values: treated as unset

- **Decision**: The run gate tests `strings.TrimSpace(h.PreflightCheck) != ""` and
  passes the raw (untrimmed) value to the executor.
- **Rationale**: The spec's edge case makes whitespace-only "treated as unset — no
  gate, status-quo behavior" for the new field. The lint guard already uses
  `strings.TrimSpace` for its emptiness test; the run gate mirrors that
  convention. The raw value is still executed verbatim because trimming would
  alter the author's command (only the emptiness decision trims).
- **Alternatives considered**: Match the nested field's bare `!= ""` test —
  rejected: a whitespace-only value would spawn `sh -c " "`, contradicting the
  spec edge case. The nested field's own behavior is deliberately left unchanged
  (FR-008 backward compatibility).

### D5 — Exclusion from `ValidateRunnerEnvWith`

- **Decision**: Do not add the top-level field to `Harness.ValidateRunnerEnvWith`;
  extend that function's doc comment (and the inline comment at the nested
  exclusion) to state that both preflight fields are gated solely by the preflight
  allowlist scan in `internal/cli`.
- **Rationale**: `ValidateRunnerEnvWith` runs before the ADR 0055 `env.runner`
  merge with a host-environment-only lookup — it cannot see harness declarations,
  so it would false-fail variables declared only in `env.runner` and false-pass
  host-set credential names. This is decision D9 of the
  preflight-check-credential-safety change, recorded in Dewey learning
  `learning/harness-fields-20261023T121014-jay-flowers`, and the spec's first
  Assumption adopts it for the top-level field.
- **Alternatives considered**: Route the field through
  `ValidateRunnerEnvWith` (the literal #5568 ask) — rejected for the reasons
  above; ADR 0128/0129 name the allowlist scan as the single reference gate for
  both checks.

### D6 — Runner-secret refusal: add the top-level field to `validateRunnerSecretRefs`

- **Decision**: In `internal/cli/runner_secrets.go`, add
  `errs.refuse("preflight_check", runnerSecretRefs(h.PreflightCheck, secrets))`
  alongside the `validation_loop.preflight_check` refusal. No change to the
  harness package's `overlayRunnerSecretRefs` (the field cannot appear in an
  overlay/forge `ForgeConfig`, per D1).
- **Rationale**: FR-006 mirrors the nested field's ADR 0136 refusal. The refusal
  happens at harness validation, before any shell executes.
- **Alternatives considered**: Fold the refusal into `runPreflightCheck` —
  rejected: the nested-field refusal lives in `validateRunnerSecretRefs`, and the
  two checks must share behavior (spec Assumption: "match their behavior rather
  than re-derive it").

### D7 — Composition: scalar merge in `mergeBaseIntoChild`, nothing in `mergeForgeConfig`

- **Decision**: In `internal/harness/compose.go` `mergeBaseIntoChild`, add
  `if child.PreflightCheck == "" { child.PreflightCheck = base.PreflightCheck }`
  in the scalar block next to `PreScript`/`PostScript`. Do not touch
  `mergeForgeConfig` in `forge.go`.
- **Rationale**: FR-009 and ADR 0129 Decision 4: scalar child-override semantics
  (child's non-empty value replaces the base's; absent child inherits; never
  appends). The compose/forge merge-function invariant (harness-composition.md,
  born from the PR #5450 six-iteration merge incident) is preserved because the
  field is *not* a ForgeConfig field: there is no forge-side merge to keep in
  sync — the documented invariant applies to fields present in both functions.
- **Alternatives considered**: Append/combine base and child commands — rejected
  by ADR 0129 (the overriding author owns covering inherited dependencies; two
  concatenated probes would be order- and shell-fragile).

### D8 — Lint: path-like guard for the top-level value + nested-field deprecation warning

- **Decision**: In `internal/harness/lint.go`: (a) add a sibling of the nested
  path-like check for `h.PreflightCheck` using the same `preflightPathLike`
  regex and message shape, Field `"preflight_check"`, SeverityWarning; (b) when
  `h.ValidationLoop.PreflightCheck` is set, emit a SeverityWarning deprecation
  diagnostic naming the top-level replacement field.
- **Rationale**: FR-007. The nested guard's shape is the template ("identical in
  shape to the nested field's guard"). The deprecation warning ships with this
  change because the replacement becomes usable when this change ships (spec
  Assumption 3); it is non-fatal, and removal of the nested field remains out of
  scope (ADR 0127 version gate).
- **Alternatives considered**: (a) SeverityError for the path-like warning —
  rejected: ADR 0128 Decision 2 keeps the diagnostic advisory in
  `fullsend lock`/`run`; fail-fast is a separately decided policy. (b) Emit the
  deprecation warning only at run time — rejected: lint is where authoring
  warnings live, and the spec says "runs or is linted" (US4 scenario 1) — lint
  surfaces it in both `fullsend lint` and run/lock paths that call `Lint()`.

### D9 — Documentation surfaces (FR-010)

- **Decision**: Update, in the same change:
  1. `docs/contributing/harness-fields.md` — "Fields that stay at top level only"
     table: drop the "**Planned, not yet implemented**" marker from the
     `preflight_check` row and state "Top level only — not a `ForgeConfig` field"
     (matching the `plugins`/`privilege_levels` rows); semantic-type table: move
     top-level `preflight_check` from "(planned)" to implemented alongside the
     nested field; merge-rules section: document scalar merge + top-level-only.
  2. `docs/reference/harness-reference.md` — new top-level field entry (command
     semantics, allowlisted environment, ordering, composition, migration note
     from the nested field).
  3. `docs/cli/lint.md` — two new diagnostics rows (top-level path-like warning;
     nested-field deprecation warning).
  4. `docs/architecture.md` — update the ADR 0129 passage (lines ~243-250) from
     "planned but not yet implemented" to current state; also refresh the stale
     adjacent ADR 0128 passage that still calls the (already-landed) nested
     path-like lint guard "planned".
  5. `docs/guides/user/customizing-agents.md` — review the
     `validation_loop.preflight_check` passage for a cross-reference to the new
     top-level field (per the AGENTS.md rule on updating doc references when
     configuration surfaces change).
- **Rationale**: FR-010 and the repo's AGENTS.md documentation rules. No
  `docs/.vitepress/config.ts` sidebar change is needed (no new pages, edits to
  existing auto-linked ones only).
- **Alternatives considered**: Defer docs to a follow-up — rejected: FR-010 is a
  MUST in the same change.

### D10 — Scaffold (`agentnew/render.go`) unchanged

- **Decision**: The new-agent scaffold keeps emitting the nested
  `validation_loop.preflight_check`. Out of scope here.
- **Rationale**: The spec's FR list does not include the scaffold; moving it to
  the top-level field is a consumer-facing choice tied to the separately tracked
  rollout (fullsend-ai/agents#1753), and the nested field continues to work
  during migration. Newly scaffolded harnesses will see the non-fatal deprecation
  warning, which is the intended migration signal.
- **Alternatives considered**: Switch the scaffold to the top-level field in this
  change — rejected: not spec'd; couples this change to consumer pin
  verification. Candidate follow-up once #1753 lands.

## Resolved clarifications (carried from spec assumptions — confirmed against code)

| Assumption | Confirmation |
|---|---|
| `${VAR}` validation satisfied by the allowlist scan, not `ValidateRunnerEnvWith` | Verified: `runPreflightCheck` → `expandPreflightCommand` rejects out-of-environment brace references before execution; `ValidateRunnerEnvWith` exclusion precedent at `harness.go` |
| Same 30s execution bound as the nested check | Verified: single `preflightCheckTimeout` var (run.go:117) shared by both call sites through `runPreflightCheck` |
| Deprecation warning non-fatal, ships with this change | Verified: `Lint()` diagnostics are advisory in `lock`/`run` (ADR 0128 Consequences) |
| #8721 and #7600 already landed for the nested field | Verified in `preflight.go`, `lint.go`, and their tests |
| Field classification already recorded as "planned" in the field reference | Verified: harness-fields.md lines 72 and 87 |
| Older CLIs silently ignore the key; schema version stays `1` | Verified: non-strict `yaml.Unmarshal`; ADR 0127 |
