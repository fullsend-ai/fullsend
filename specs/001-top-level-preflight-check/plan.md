# Implementation Plan: Top-Level Preflight Check

**Branch**: `001-top-level-preflight-check` | **Date**: 2026-10-26 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/001-top-level-preflight-check/spec.md`

## Summary

Add a top-level `preflight_check` field to the harness — an optional string holding a
literal host `sh -c` command that runs exactly once per run, before `pre_script` and
before sandbox creation, as the single eager host-dependency gate for `pre_script`,
`post_script`, and `validation_loop` (ADR 0129). The field reuses, unchanged, the
credential-safety machinery that already gates the nested
`validation_loop.preflight_check` (issue #8721): the allowlisted preflight environment,
pre-execution `${VAR}` reference rejection, runner-secret refusal, diagnostic
redaction, and the 30-second execution bound, all centralized in
`internal/cli/preflight.go`'s `runPreflightCheck`. It merges as a scalar in `base:`
composition (child overrides base, never appends), is settable only at the harness
top level (a key inside `forge:` or `overlays:` is ignored), and ships with a
deprecation lint warning on the nested field plus the same path-like authoring
warning the nested field has. The nested field keeps running — after the top-level
check — so existing harnesses are unaffected (schema version stays `1` per ADR 0127).

## Technical Context

**Language/Version**: Go 1.26.5 (repo `go.mod`; toolchain matches on this machine)

**Primary Dependencies**: `gopkg.in/yaml.v3` (harness unmarshal), `github.com/stretchr/testify`
(tests), `github.com/google/cel-go` (overlays, unchanged). No new dependencies.

**Storage**: N/A — the change adds one YAML-declared harness field (in-memory `Harness`
struct member); no persistence, database, or file-format change. The harness YAML is a
cross-repository API boundary (ADR 0127) but remains schema version `1`: an older
loader silently ignores the added key.

**Testing**: Go stdlib `testing` + testify, run with `go test ./...` (per `AGENTS.md`,
prefer `go run ./cmd/fullsend` for CLI invocation). Existing test scaffolding to
mirror: `preflightTestSetup` / `useFakeOpenshell` / `usePreScriptStub` helpers and the
overridable `preflightCheckTimeout` var in `internal/cli`. Go production changes must
meet the 80% patch coverage target (75% enforced floor) — verify locally before
committing.

**Target Platform**: Cross-platform Go CLI (GitHub Actions Linux/macOS runners,
darwin/arm64 development).

**Project Type**: CLI (`cmd/fullsend`) with `internal/` packages; this feature touches
`internal/harness` (schema, merge, lint) and `internal/cli` (run gate, secret refusal).

**Performance Goals**: The check adds one bounded host subprocess per run
(`preflightCheckTimeout`, 30s today) that runs before any expensive stage (sandbox
creation, agent inference); its purpose is to *save* time by failing fast.

**Constraints**:
- Credential safety is a release gate (ADR 0128/0129, issue #8721): the top-level
  check must run under the same allowlisted environment, pre-execution reference
  rejection, runner-secret refusal, and diagnostic redaction as the nested check.
- Backward compatibility: harnesses without the field behave exactly as before;
  the nested field continues to run during migration (FR-008); schema version
  stays `1`.
- Compose/forge merge-function invariant: `mergeBaseIntoChild` and
  `mergeForgeConfig` must stay behaviorally aligned per field; the new field is
  scalar in composition and absent from `ForgeConfig` (top-level only).
- The field must NOT participate in `Harness.ValidateRunnerEnvWith` (host-env
  validation cannot see harness declarations; the preflight allowlist scan in
  `internal/cli` is the single reference gate — decision D9 of the
  preflight-check-credential-safety change, confirmed by Dewey learnings
  `learning/harness-fields-20261023T121014-jay-flowers` and
  `learning/preflight-check-20261022T201221-jay-flowers`).

**Scale/Scope**: ~4 production files (`internal/harness/harness.go`,
`internal/harness/compose.go`, `internal/harness/lint.go`, `internal/cli/run.go`,
`internal/cli/runner_secrets.go`), ~7 test files, 5 documentation files. Small,
precedent-rich change: every mechanism already exists for the nested field.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| # | Principle (constitution) | Assessment | Verdict |
|---|---------------------------|------------|---------|
| I | Threat-first security — external inputs validated before security-sensitive operations | The field's value is untrusted harness content executed on the host via `sh -c`. The plan routes it through the existing single reference gate (`runPreflightCheck`): allowlisted minimal environment, `${VAR}` rejection before any shell runs, runner-secret refusal at validation, redacted failure/timeout diagnostics. No new credential surface. | PASS |
| II | The repo is the coordinator | Behavior is proven only by repo gates: sentinel-action tests, the coverage check, lint, and review. No runtime coupling or agent assertions. | PASS |
| III | Code generation is a solved problem — review path matters | Every non-obvious choice below cites its ADR or repo precedent and is recorded in [research.md](research.md) for review. | PASS |
| IV | Forge abstraction — all forge operations via `forge.Client` | No forge API operations are added or changed; the feature is harness-schema and run-path only. | PASS |
| V | Per-repo installation | No org-mode content; per-repo harness semantics only. | PASS |
| VI | Organization-agnostic core | All doc updates stay organization-agnostic; consumer-rollout specifics remain in fullsend-ai/agents#1753, out of scope. | PASS |
| — | Coverage gate (80% target / 75% floor) | Tasks include local patch-coverage verification before commit. | PASS (planned) |
| — | COMMITS.md / DCO conventions | PR title uses a Conventional Commit prefix; human-directed commits carry `-s` and `AI-Assisted: true` per local conventions. | PASS (planned) |

No violations. **Post-Phase-1 re-check**: the design in [data-model.md](data-model.md)
and [contracts/harness-preflight-check.md](contracts/harness-preflight-check.md) adds
no schema version bump, no new execution surface (one additional call to the existing
credential-safe executor), and no conditional setter — verdicts unchanged, all PASS.

## Project Structure

### Documentation (this feature)

```text
specs/001-top-level-preflight-check/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
│   └── harness-preflight-check.md
├── checklists/          # Pre-existing spec analysis artifacts
│   └── requirements.md
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
internal/
├── harness/                     # Harness schema, composition, lint
│   ├── harness.go               # Harness.PreflightCheck field (near PreScript/PostScript);
│   │                            #   ValidateRunnerEnvWith doc comment covers both preflight fields
│   ├── compose.go               # mergeBaseIntoChild: scalar merge (child non-empty wins)
│   ├── forge.go                 # UNCHANGED — ForgeConfig deliberately gains no field (top-level only)
│   ├── lint.go                  # Path-like guard for the top-level value; nested-field deprecation warning
│   ├── harness_test.go          # Load test; ValidateRunnerEnvWith skip test for the top-level field
│   ├── compose_test.go          # Scalar-override, inheritance, overlay-selected-script, ignored-key tests
│   ├── forge_test.go            # mergeForgeConfig non-settability test (compose/forge invariant)
│   └── lint_test.go             # Path-like warning, literal-command pass, deprecation warning tests
├── cli/
│   ├── run.go                   # Top-level gate: runPreflightCheck call inserted before the nested check
│   ├── preflight.go             # UNCHANGED — shared executor (source label param already parameterizes it)
│   ├── runner_secrets.go        # validateRunnerSecretRefs: refuse runner-secret refs in the top-level field
│   ├── run_test.go              # Sentinel-action tests: ordering, failure, timeout, skip path, both-fields
│   ├── preflight_test.go        # Unit-level mirrors of the nested check's credential-safety tests
│   ├── runner_secrets_test.go   # Refusal test entry for the top-level field
│   └── lint_test.go             # CLI lint command surfacing of the two new diagnostics
└── agentnew/
    └── render.go                # UNCHANGED — scaffold keeps the nested field (see research.md D10)

docs/
├── contributing/harness-fields.md   # Classification + semantic-type tables: drop "planned" markers;
│                                    #   merge-rule documentation for the scalar, top-level-only field
├── reference/harness-reference.md   # New top-level field section; nested-field deprecation/migration note
├── cli/lint.md                      # Two new lint diagnostics rows
├── architecture.md                  # Update the ADR-0129 passage from "planned" to current state
└── guides/user/customizing-agents.md # Cross-reference review for the new field (per AGENTS.md doc rule)
```

**Structure Decision**: The change follows the repo's existing single-module layout
(`internal/harness` owns schema/merge/lint; `internal/cli` owns execution and
credential gates). No new packages: the field is one `Harness` member whose behavior
is delegated to the existing `runPreflightCheck` executor, keeping the
compose/forge merge invariant and the single-gate credential design intact.

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

No constitution violations — table intentionally empty.
