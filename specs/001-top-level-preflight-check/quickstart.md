# Quickstart: Top-Level Preflight Check

**Feature**: [spec.md](spec.md) | **Branch**: `001-top-level-preflight-check` | **Date**: 2026-10-26

Runnable validation scenarios proving the feature works end-to-end. Field
semantics, environment, merge, and diagnostic contracts:
[contracts/harness-preflight-check.md](contracts/harness-preflight-check.md);
entity and ordering model: [data-model.md](data-model.md).

## Prerequisites

- Go 1.26.5 toolchain (`go version`).
- Repo root as working directory for all commands.
- No network or forge credentials needed — every scenario below runs hermetically
  against local fixtures (the test suite fakes openshell and stubs sandbox
  creation).

## 1. Automated verification (primary)

Run the focused suites that own this feature:

```bash
go test ./internal/cli/ -run 'TestRunAgent_PreflightCheck|TestRunPreflightCheck|TestValidateRunnerSecretRefs' -v
go test ./internal/cli/ -run 'TestLintCommand' -v
go test ./internal/harness/ -run 'PreflightCheck|Lint' -v
```

Expected outcomes (assertions the new tests must carry — see
[data-model.md](data-model.md) for the ordering model):

- **Ordering (US1-1, FR-011)**: a passing top-level check runs exactly once,
  before `pre_script` and before sandbox creation — proven with sentinel files
  written by the check, the pre-script, and the (failing) sandbox stub, asserting
  file-order; the `usePreScriptStub` pattern makes sandbox creation fail loudly
  so reaching a later error proves the earlier stage ran.
- **Failure path (US1-2, FR-003)**: a nonzero exit (and, with
  `preflightCheckTimeout` shortened to ~50ms, a timeout) returns an error
  containing `preflight_check failed` / `preflight_check timed out`, and no
  pre-script, sandbox, or nested-check sentinel exists.
- **Skip path (US1-5, FR-011)**: a pre-script that requests skip (exit 78) still
  observes the top-level check's sentinel — the check ran before the skip
  decision.
- **Both fields (US4-2, FR-008)**: top-level sentinel precedes the nested
  sentinel; a failing top-level check leaves the nested sentinel absent.
- **Status quo (US1-4, SC-002)**: harnesses without the top-level field behave
  as before — the pre-existing `TestRunAgent_PreflightCheck_*` family still
  passes unchanged.
- **Credential safety (US2, FR-004/005/006)**: an out-of-allowlist `${VAR}`
  reference fails pre-execution naming the variable; declared token values are
  redacted from failure/timeout diagnostics; the child environment excludes
  refused credential names; a runner-secret reference is refused at validation.
- **Composition (US3, FR-009/FR-012)**: child override yields exactly one
  effective gate value (child's); absent child inherits the base's; an
  overlay-selected script dependency stays gated by the top-level check; a
  `preflight_check` key under `forge:`/`overlays:` is ignored.
- **Lint (FR-007)**: a path-like top-level value warns; a literal command does
  not; a set nested field emits the deprecation warning naming the top-level
  replacement.

Then the full gate suite (per `AGENTS.md`):

```bash
go build ./... && go vet ./...
go test ./...
```

All green, including the untouched pre-existing nested-field tests.

## 2. Manual end-to-end scenario (local harness)

1. Create a scratch `.fullsend` directory. Two fixture requirements: the
   harness needs a `policy:` file on disk (every sandbox harness does), and
   the commands below use the `--fullsend-dir` flag (there is no `--dir`):

```bash
TMP=$(mktemp -d)
mkdir -p "$TMP/harness" "$TMP/agents" "$TMP/policies"
printf 'You are a test agent.\n' > "$TMP/agents/code.md"
printf 'version: 1\n' > "$TMP/policies/base.yaml"
printf 'agents:\n  - harness/code.yaml\n' > "$TMP/config.yaml"
cat > "$TMP/harness/code.yaml" <<'EOF'
agent: agents/code.md
role: test
policy: policies/base.yaml
preflight_check: 'test -x /nonexistent-tool'
EOF
```

2. Run the agent against it from the repo root (`run` requires
   `--target-repo`; `.` points it at this checkout):

```bash
go run ./cmd/fullsend run --fullsend-dir "$TMP" --target-repo . code 2>&1 | grep preflight_check
```

**Expected**: the run stops immediately with a `preflight_check failed` error
naming the missing dependency — before any sandbox activity. Change the check to
`'true'` and re-run: the preflight step reports passed and the run proceeds
(failing later at openshell/gateway on a machine without the sandbox stack,
which proves the gate no longer blocks).

3. Lint scenario (`lint` takes no agent argument — it lints every enabled
   agent in the config):

```bash
go run ./cmd/fullsend lint --fullsend-dir "$TMP"
```

With `preflight_check: scripts/common-preflight.sh` in the harness, expect the
path-like authoring warning for `preflight_check`. Adding a
`validation_loop:` block with a nested `preflight_check:` key (its `script:`
must exist on disk, e.g. `scripts/validate.sh`) yields the nested-field
deprecation warning naming the top-level replacement:

```yaml
validation_loop:
  script: scripts/validate.sh
  preflight_check: 'true'
```

## 3. Patch coverage gate (before committing)

Go production changes must meet the 80% patch coverage target (75% enforced
floor). Use the repo skill after writing/updating Go code:

- Follow `.agents/skills/check-patch-coverage/SKILL.md`
  (approximate-local-coverage flow) and re-check after each test addition.

## 4. Definition of done for verification

- [x] Focused suites (§1) pass with the assertions above.
- [x] `go test ./...` green (no regression in nested-field or composition behavior).
      Verified 2026-10-26: the only failures are the 15 pre-existing
      environmental ones from the T001 baseline (forge/gitlab 1, repos 1,
      runtime 11, scaffold 2) — no new failures; internal/cli and
      internal/harness pass fresh.
- [x] Manual scenario (§2) shows eager gating and deprecation diagnostics.
- [x] Patch coverage ≥ 80% target (≥ 75% floor).
      Verified 2026-10-26: 100% patch coverage — 22/22 executable added
      lines (run.go 4, lint.go 14, compose.go 3, runner_secrets.go 1;
      harness.go additions are field declaration + doc comments).
- [x] Documentation updated per [research.md](research.md) D9 (field tables,
      harness reference, lint reference, architecture, user-guide cross-reference).
