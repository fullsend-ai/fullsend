# Data Model: Top-Level Preflight Check

**Feature**: [spec.md](spec.md) | **Branch**: `001-top-level-preflight-check` | **Date**: 2026-10-26

Entities extracted from the feature spec. Go type names reference the production
types this change adds or extends; the YAML spellings are the cross-repository
contract (ADR 0127).

## Entity: Harness Preflight Check (top-level)

The single eager host-dependency gate for `pre_script`, `post_script`, and
`validation_loop`.

| Aspect | Value |
|---|---|
| YAML key | `preflight_check` (harness top level only) |
| Go representation | `Harness.PreflightCheck string` with `yaml:"preflight_check,omitempty"` |
| Type | Optional string; literal host command (semantic type: **inline command**, ADR 0127/0128) |
| Default | Absent — no gate, status-quo behavior |
| Settable via `forge:` / `overlays:` | **No** — `ForgeConfig` has no such field; the key is silently ignored there |
| Working directory | None set — relative paths resolve against the runner's working directory |
| Resource resolution | Never — the value is passed verbatim to `sh -c`, never fetched like `pre_script` |

### Validation rules (from FR-001…FR-009)

- **Emptiness**: absent, empty, or whitespace-only ⇒ treated as unset (run gate
  tests `strings.TrimSpace != ""`; raw value is executed verbatim otherwise).
- **Reference validity** (pre-execution, FR-005): every `${VAR}` brace reference
  must resolve in the preflight environment (below); otherwise the run fails
  before any shell executes, the error names the variable (never a value), and
  points to the runner-env declaration channel. Bare `$VAR` references are left
  to the child shell (unset ⇒ empty). Non-plain brace forms (`${VAR:-default}`)
  are not validated.
- **Runner-secret refusal** (FR-006, ADR 0136): any reference to a runner-secret
  name is refused at harness validation.
- **NOT validated by** `Harness.ValidateRunnerEnvWith` — the allowlist scan in
  the run path is the single reference gate (research.md D5).
- **No structural `Validate()` rule**: an optional scalar; path-like and other
  authoring concerns are lint diagnostics, not load failures.

## Entity: Preflight Environment (shared by both checks)

The variable set the command's `${VAR}` references validate against and the
child `sh -c` process runs with. Unchanged from the nested field's #8721 design;
both call sites build it through the same `preflightEnv`.

| Source | Members | Collision rule |
|---|---|---|
| Host allowlist | `PATH`, `HOME`, `TMPDIR` (only when set in the host process) | Harness declaration wins |
| Runner-forced | `FULLSEND_DIR` (= absFullsendDir), `PYTHONDONTWRITEBYTECODE=1` | Pinned; a harness declaration of either is dropped |
| Harness declarations | Non-credential `env.runner` / `runner_env` values (post ADR-0055 merge) | — |
| Refused (never present, even when declared) | Minted-token inventory (`GITLAB_TOKEN`, forge token, `FULLSEND_FETCH_TOKEN`, per-role agent tokens, minted role-token vars), `harnessExpansionDenied` set (OIDC request vars, `OPENAI_API_KEY`, `GH_WORKFLOW_TOKEN`, trigger and runner-secret names), GitLab role-routing prefix | Excluded from child env and from reference validation |

## Entity: Run Lifecycle (state transitions with the gate)

The run path around the gate (anchors are `internal/cli/run.go`):

```text
load + compose + Validate  ──►  [NEW] top-level preflight_check
        │                              │ pass (exit 0 within bound)
        │                              ▼
        │                      nested validation_loop.preflight_check   (existing "1d")
        │                              │ pass
        │                              ▼
        │                      openshell availability + gateway checks
        │                              │
        │                              ▼
        │                      pre_script   (may request skip; exit 78)
        │                              │
        │                              ▼
        │                      sandbox.CreateWithRetry ── agent ── validation ── post_script
```

Transition rules (FR-002, FR-003, FR-008; US1/US4):

| From state | Event | To state |
|---|---|---|
| Run start | top-level check exit 0 (within 30s bound) | nested check (if set) → continue |
| Run start | top-level check nonzero exit | **terminal**: error `preflight_check failed: <redacted cmd>\n<redacted output>` — no pre_script, no sandbox, no nested check |
| Run start | top-level check exceeds bound | **terminal**: error `preflight_check timed out after 30s: <redacted cmd>` — same gating |
| Run start | brace reference outside preflight environment | **terminal** before any shell: error names `${VAR}` and the declaration channel |
| Run start | runner-secret name referenced | refused earlier, at harness validation |
| Nested check | runs only after top-level pass | failure semantics unchanged (status quo) |
| pre_script | requests skip (exit 78) | run reports skipped — the top-level check has **already run** (eager-gate trade-off, US1 scenario 5) |
| No top-level field | — | exact status-quo behavior including any nested check |

Field-qualified error identity: the `source` label is `"preflight_check"` for the
new gate and remains `"validation_loop.preflight_check"` for the nested gate, so
diagnostics always name the field whose command failed.

## Entity: Composition Merge (scalar)

`mergeBaseIntoChild` semantics for the field (FR-009; ADR 0129 Decision 4):

| Base value | Child value | Composed value |
|---|---|---|
| unset | unset | unset |
| `A` | unset | `A` (inherit) |
| `A` | `B` | `B` (child overrides — never `A && B`, never concatenation) |
| unset | `B` | `B` |

Constraints:

- The overriding child author is responsible for `B` covering the dependencies of
  any inherited `pre_script`, `post_script`, and validation script still present
  in the composed harness (documented contract, not enforced).
- Overlay-selected stage scripts do not move the check: the single top-level gate
  covers whichever scripts the overlays selected; the author must cover the union
  of dependencies across possible matching overlays.
- No forge-side merge exists (field absent from `ForgeConfig`); a
  `preflight_check` key under `forge:`/`overlays:` is ignored.

## Entity: Diagnostics

| Diagnostic | Surface | Severity | Shape |
|---|---|---|---|
| Path-like top-level value | `Harness.Lint()` → `fullsend lint`/`run`/`lock` | Warning (non-fatal) | Field `preflight_check`; message identical in shape to the nested guard: value looks like a script path; runs verbatim as `sh -c`; never fetched; use `pre_script` or inline the command |
| Nested field deprecation | `Harness.Lint()` | Warning (non-fatal) | Field `validation_loop.preflight_check`; names the top-level replacement field |
| Run failure | run-path error | fatal (run stops) | `preflight_check failed: <redacted cmd>\n<redacted output>\nInstall the missing dependency…` |
| Run timeout | run-path error | fatal (run stops) | `preflight_check timed out after 30s: <redacted cmd>` |
| Out-of-allowlist `${VAR}` | run-path error, pre-execution | fatal (run stops) | names the reference, the allowlist, and the `env.runner` declaration channel; never a value |
| Runner-secret reference | harness validation error | fatal (load refuses) | names the field `preflight_check` and the refused secret name(s) |

## Relationships

- `Harness` **1 — 0..1** top-level `preflight_check` (scalar member).
- `Harness` **1 — 0..1** `ValidationLoop`, which **1 — 0..1** nested
  `preflight_check` (existing; deprecated once this feature ships, removal out of
  scope per ADR 0127).
- Both checks **share** the Preflight Environment entity and the single executor
  (`runPreflightCheck`), including its timeout and redaction behavior.
- The top-level field participates in `base:` composition (scalar) and in **no**
  conditional channel (`forge:`/`overlays:`).
