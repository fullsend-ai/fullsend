# Feature Specification: Top-Level Preflight Check

**Feature Branch**: `001-top-level-preflight-check`

**Created**: 2026-10-26

**Status**: Implemented (2026-10-26 — all 36 tasks complete; code review APPROVE WITH ADVISORIES)

**Input**: User description: "Add top-level preflight_check to Harness, running before pre_script and sandbox creation" — resolved from GitHub issue [#8722](https://github.com/fullsend-ai/fullsend/issues/8722) (epic #8720).

**References**:

- Issue: [#8722](https://github.com/fullsend-ai/fullsend/issues/8722) — this spec implements it; epic [#8720](https://github.com/fullsend-ai/fullsend/issues/8720).
- Design authority: [ADR 0129 — Extend preflight coverage to pre_script and post_script](../../docs/ADRs/0129-extend-preflight-coverage-to-pre-and-post-scripts.md) (Accepted). Supersedes the PR [#6009](https://github.com/fullsend-ai/fullsend/pull/6009) design; resolves the design question tracked in [#5568](https://github.com/fullsend-ai/fullsend/issues/5568) (closed as resolved).
- Field semantics: [ADR 0128 — preflight_check is a literal host command, not a script resource](../../docs/ADRs/0128-preflight-check-literal-command.md).
- Release gates that must hold: [#8721](https://github.com/fullsend-ai/fullsend/issues/8721) (credential-safety gates) and [#7600](https://github.com/fullsend-ai/fullsend/issues/7600) (Lint path-pattern guard) — both already landed for the nested field; this change must match their behavior for the new field.
- Outage motivating the coverage gap: [#5074](https://github.com/fullsend-ai/fullsend/issues/5074) (2026-09-21 preflight_check outage).
- Consumer rollout and harness migration: [fullsend-ai/agents#1753](https://github.com/fullsend-ai/agents/issues/1753) (tracked separately; out of scope here).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Declare one early host-dependency gate (Priority: P1)

As a harness author, I want a top-level `preflight_check` field that runs once, before `pre_script` and before sandbox creation, so that a missing host dependency for any stage — pre-script, post-script, or validation — fails the run immediately, before any sandbox is created or any agent work is spent.

**Why this priority**: This is the core deliverable of ADR 0129. Today a dependency probe can only be declared under `validation_loop`, so no field semantically owns pre-script and post-script dependencies. A post-script dependency that is missing is discovered only after the agent has finished and teardown has run — the most expensive possible failure point — which is exactly the gap ADR 0129 closes. Without this story there is no feature.

**Independent Test**: A harness that sets only a top-level `preflight_check` whose command exits nonzero stops the run before sandbox creation with an error naming the failed check. This delivers value on its own: the eager host-dependency gate.

**Acceptance Scenarios**:

1. **Given** a harness whose top-level `preflight_check` command exits zero, **When** a run starts, **Then** the command runs exactly once, before `pre_script` (when present) and before sandbox creation, and the run proceeds.
2. **Given** a harness whose top-level `preflight_check` command exits nonzero, **When** a run starts, **Then** the run stops with an error naming the failed preflight check, and no `pre_script` runs, no sandbox is created, and no nested `validation_loop.preflight_check` runs.
3. **Given** a harness whose top-level `preflight_check` command does not terminate, **When** the check exceeds its execution bound, **Then** the run stops with a timeout report and the same gating as a nonzero exit.
4. **Given** a harness with no top-level `preflight_check`, **When** a run starts, **Then** behavior is exactly as before this feature, including any nested `validation_loop.preflight_check` (backward compatible).
5. **Given** a harness whose `pre_script` requests a run skip, **When** a run starts, **Then** the top-level check has already run before the skip decision was made — the eager gate intentionally checks post-script dependencies even on runs the pre-script would skip.

---

### User Story 2 - The gate is credential-safe and misuse-guarded (Priority: P1)

As a run operator, I want the top-level check to run under the same credential-safety gates and lint guardrails as the existing nested check, so that adding the field cannot leak tokens into a host command, its environment, or its diagnostics.

**Why this priority**: ADR 0129 makes ADR 0128's restricted credential environment, reference rejection, and diagnostic redaction a release requirement for the top-level field — it must hold for **both** checks before the top-level field is released. Issue #8721 is the named release gate for this change; under the project's threat-first constitution, security outranks convenience. The field does not ship without these behaviors.

**Independent Test**: A harness whose top-level check references a credential name or an out-of-allowlist variable is refused before any shell executes; a failing or timing-out check whose declared runner-env values are secrets reports only redacted command text and output. Each assertion is verifiable through focused validation and run scenarios.

**Acceptance Scenarios**:

1. **Given** a harness whose top-level `preflight_check` contains a `${VAR}` brace reference to a name outside the allowlisted preflight environment, **When** the run starts, **Then** the run fails before any shell executes the command, and the error names the variable (never its value) and points to the runner-env declaration channel.
2. **Given** a harness that declares a runner-env variable whose value is a secret, **When** the top-level check fails or times out, **Then** the reported command text and output have the secret redacted.
3. **Given** a harness whose top-level `preflight_check` runs, **When** its child process inspects its environment, **Then** it sees only the allowlisted host names (`PATH`, `HOME`, `TMPDIR` when set), the runner-forced `FULLSEND_DIR` and `PYTHONDONTWRITEBYTECODE`, and non-credential variables declared via `env.runner` (or the deprecated `runner_env`) — forge, mint, and provider credential names, runner secrets, and OIDC/trigger names never reach the child, even when declared.
4. **Given** a harness whose top-level `preflight_check` value looks like a bare script path (e.g. `scripts/common-preflight.sh`), **When** the harness is linted, **Then** a diagnostic flags the value as looking like a script path and explains that the check runs verbatim as a shell command and is never fetched or resolved — a non-fatal authoring warning, identical in shape to the nested field's guard.

---

### User Story 3 - Compose and override the gate (Priority: P2)

As a harness author using `base:` composition, I want the top-level `preflight_check` to merge as a scalar — a child's value replaces the base's and never appends — so that a composed harness has exactly one predictable gate whose declared command is the one that runs.

**Why this priority**: Composition is how shared harnesses are distributed and consumed across repositories. Without defined merge semantics the field silently corrupts during composition — the compose/forge merge-function invariant exists because of a real six-iteration merge incident (PR #5450). It is P2 because the field is fully useful standalone before composition semantics matter.

**Independent Test**: Compose a child over a base where both set `preflight_check`; the composed harness carries the child's command only, proving scalar override. Also compose a child that sets nothing and observe inheritance of the base's command.

**Acceptance Scenarios**:

1. **Given** a base harness with `preflight_check: A` and a child that does not set the field, **When** they are composed, **Then** the composed harness inherits A.
2. **Given** a base harness with `preflight_check: A` and a child that sets `preflight_check: B`, **When** they are composed, **Then** the composed harness carries B only — the override does not append to or combine with A. The overriding author is responsible for B covering the dependencies of any inherited `pre_script`, `post_script`, and validation script still present in the composed harness.
3. **Given** a harness whose overlays select different stage scripts per condition while `preflight_check` stays top-level, **When** the harness runs, **Then** the single top-level check gates whichever scripts the overlays selected — the author must cover the union of dependencies across possible matching overlays, and a dependency of an inactive overlay can fail an otherwise eligible run.
4. **Given** a `preflight_check` key placed inside a `forge:` block or an `overlays:` entry, **When** the harness is loaded, **Then** the key is ignored — the field is top-level only, and conditional values cannot replace the gate.

---

### User Story 4 - Migrate away from the nested field (Priority: P3)

As a harness author maintaining existing harnesses that set `validation_loop.preflight_check`, I want the nested field to keep working and to tell me it is deprecated, so I can move my probe to the top-level field on my own schedule without a broken run.

**Why this priority**: Migration safety is required by ADR 0129 but delivers no new capability. The migration window must be safe (nested checks continue to run), and removal is explicitly out of scope — it requires a separately decided compatibility/version gate (ADR 0127).

**Independent Test**: A harness with only a nested `validation_loop.preflight_check` runs exactly as before, plus a deprecation warning naming the replacement; a harness with both fields runs the top-level check first and then the nested one.

**Acceptance Scenarios**:

1. **Given** a harness that sets only `validation_loop.preflight_check`, **When** it runs or is linted, **Then** it behaves exactly as before this feature, plus a deprecation warning that names the top-level replacement field.
2. **Given** a harness that sets both the top-level `preflight_check` and `validation_loop.preflight_check`, **When** a run starts, **Then** the top-level check runs first and the nested check runs after it; a failing top-level check prevents the nested check from running at all.
3. **Given** this feature has shipped (the replacement is usable), **When** any harness sets the nested field, **Then** the deprecation warning is emitted. Removal of the nested field does not happen in this change.

---

### Edge Cases

- What happens when both the top-level and nested checks are configured? The top-level check runs first; the nested check runs only if the top-level passed. Both are subject to the same credential-safety gates (allowlist, reference rejection, redaction).
- What happens when the top-level check passes but the nested check fails? The run stops at the nested check, exactly as before this feature — nested failure semantics are unchanged.
- What happens when a run would be skipped by `pre_script`? The top-level check has already run. The eager gate intentionally checks post-script dependencies even on runs the pre-script would skip; authors must account for probes that fail only on skip-eligible runs (the ADR 0129 trade-off).
- What happens when the command references a variable with a bare `$VAR` (no braces)? It is left to the child shell, where unset means empty — matching the nested check's semantics. Only brace references are validated before execution.
- What happens when the command value uses a non-plain brace form such as `${VAR:-default}`? It is not validated and expands per shell semantics, matching the nested check's documented behavior.
- What happens when the value is path-like (`scripts/check.sh`)? A lint diagnostic warns; the value still executes verbatim as a shell command at run time and is never fetched or resolved like `pre_script`.
- What happens when the value is empty or whitespace-only? It is treated as unset — no gate, status-quo behavior.
- What happens when a runner-secret name appears in the field's references? It is refused at harness validation, mirroring the nested field's refusal.
- What happens when the field is consumed by an older CLI pinned in a consumer repository? The key is silently ignored and the run proceeds without the gate. Publishing a harness whose safety depends on the check therefore requires consumer pin verification first (tracked in fullsend-ai/agents#1753; out of scope here).

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST provide a top-level `preflight_check` field on the harness — an optional string. An absent or empty value MUST leave run behavior exactly as before this feature.
- **FR-002**: When set, the system MUST execute the value as a literal shell command (`sh -c`) exactly once per run, before `pre_script` (when present) and before sandbox creation. The value MUST NOT be resolved through the harness resource-fetch pipeline, and the command runs without a working directory being set, so relative paths resolve against the runner's working directory.
- **FR-003**: A nonzero exit or an execution timeout of the top-level check MUST stop the run before `pre_script`, before sandbox creation, and before any nested `validation_loop.preflight_check`, reporting which check failed with field-qualified, redacted command text and output.
- **FR-004**: The command MUST run in the credential-safe allowlisted preflight environment: the host `PATH`, `HOME`, and `TMPDIR` (when set), the runner-forced `FULLSEND_DIR` and `PYTHONDONTWRITEBYTECODE`, and non-credential variables declared via `env.runner` (or the deprecated `runner_env`). Credential names — forge, mint, and provider tokens, runner secrets, and OIDC/trigger names — MUST be excluded even when declared.
- **FR-005**: `${VAR}` brace references in the command MUST be validated against that merged preflight environment (the runner environment merged with the allowlist) and rejected before any shell executes when a referenced name is outside it. The rejection error MUST name the variable, never a value, and MUST point to the runner-env declaration channel.
- **FR-006**: Runner-secret names MUST be refused in the field's references at harness validation, mirroring the nested field's refusal.
- **FR-007**: Harness lint MUST flag a bare path-like top-level `preflight_check` value with the same diagnostic shape as the nested field's guard (non-fatal authoring warning), and MUST emit a deprecation warning when `validation_loop.preflight_check` is set, naming the top-level replacement.
- **FR-008**: During migration, a nested `validation_loop.preflight_check` MUST continue to run — after the top-level check when both are configured — and harnesses that set only the nested field MUST retain their existing behavior.
- **FR-009**: In `base:` composition the field MUST merge as a scalar: the child's non-empty value replaces the base's; an absent child value inherits the base's; values MUST NOT append or combine. The field MUST NOT be settable through conditional values (`forge:` blocks or `overlays:` entries) — a key there is ignored, never honored.
- **FR-010**: Documentation MUST be updated per the harness-field conventions: the field-classification and semantic-type tables (removing the "planned, not yet implemented" marker for this field), the merge-rule documentation, and the harness reference page for the new field, including the nested-field migration note.
- **FR-011**: Automated tests MUST demonstrate, using sentinel actions, the ordering (the check precedes `pre_script` and sandbox creation), the failure path (a nonzero exit or timeout gates all later stages, including the nested check), and the skip path (the check runs before a pre-script skip decision).
- **FR-012**: Automated composition tests MUST cover a child override with an inherited script dependency and an overlay-selected script dependency.

### Key Entities

- **Top-level `preflight_check`**: Optional string harness field; a literal host command; the single eager host-dependency gate for `pre_script`, `post_script`, and `validation_loop`. Merges as a scalar in composition; top-level only (not conditionally settable).
- **Preflight environment**: The minimal allowlisted variable set the command expands against and runs with — host allowlist plus runner-forced names plus declared non-credential runner variables. Shared by the top-level and nested checks.
- **Nested `validation_loop.preflight_check`**: The existing per-loop probe. Continues to run after the top-level check during migration; deprecated in favor of the top-level field once this feature ships; removal out of scope.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A harness whose top-level check fails stops the run before sandbox creation and before any stage script in 100% of tested scenarios — the missing dependency is reported at the start of the run, not after the agent completes.
- **SC-002**: Harnesses that do not use the field behave exactly as before this feature: the existing test suite passes with no behavioral regressions (backward compatible).
- **SC-003**: No credential value reaches the check's child environment or appears unredacted in its failure, timeout, or validation diagnostics in 100% of the credential-exclusion test scenarios.
- **SC-004**: Every composed-harness scenario yields exactly one effective gate value, resolved by scalar child-override semantics with no append or combination, in 100% of composition tests.
- **SC-005**: Every harness that sets only the nested field completes its run as before, with a deprecation warning, in 100% of migration scenarios.

## Assumptions

- The `${VAR}` validation ask carried from #5568 ("validated via ValidateRunnerEnvWith") is satisfied by the preflight allowlist scan that gates the nested field today (the #8721 design): references are validated before execution against the preflight environment, which is the runner environment merged with the allowlist. The field deliberately does **not** participate in the generic host-environment variable validation — that check cannot see harness declarations, so it would both false-fail variables declared only in `env.runner` and false-pass host-set credential names. ADR 0128/0129 name the allowlist scan as the single reference gate for both checks, and this change follows that decision.
- Execution bound: the top-level check uses the same execution timeout as the nested check (30 seconds today).
- The deprecation warning for the nested field is a non-fatal lint warning, emitted whenever the nested field is set, shipping with this change — the replacement becomes usable when this change ships.
- Nested-field removal, a separately named resource-resolved `preflight_script` variant, and choosing a concrete minimum consumer CLI release are out of scope (ADR 0127/0128 follow-ups, separately decided).
- Consumer rollout — verifying that every pinned fullsend consumer executes the field before harnesses rely on it — is tracked in fullsend-ai/agents#1753 and is out of scope for this change beyond the backward-compatible YAML addition (older loaders silently ignore the key; the schema version stays `1` per ADR 0127).
- The release gate #8721 (credential safety) and the Lint guard #7600 have already landed for the nested field; this change must match their behavior for the new field rather than re-derive it.
- The field classification (top-level only, inline-command semantic type, scalar merge) is already recorded in the harness field reference as "planned"; this change implements it and updates those docs to current state.
