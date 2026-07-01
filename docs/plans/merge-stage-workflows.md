# ADR 62: Inline stages into reusable-dispatch.yml (per-repo only)

## Context

Per-repo mode has a version skew problem: `reusable-dispatch.yml` references `reusable-{stage}.yml@v0` via
hardcoded `uses:` lines. During development/testing, changes to stage workflows aren't picked up because `@v0` points
to the released version. ADR 62 decides to inline all stage logic into `reusable-dispatch.yml`, eliminating
the second `uses:` hop.

Per-org mode is unaffected (its flow goes through scaffold `dispatch.yml` → `gh workflow run` →
thin callers → `reusable-{stage}.yml@v0`) and must not change. `reusable-{stage}.yml` files stay for
per-org until it's removed per ADR 44.

## Plan

Branch from main. Per-org scaffold files stay untouched — only `reusable-dispatch.yml` and the new composite
action are changed.

### 1. Inline stage logic into reusable-dispatch.yml

Inline each `reusable-{stage}.yml` job directly into `reusable-dispatch.yml`, eliminating the
`uses: reusable-{stage}.yml@v0` hop. Verify:
- Route job logic matches main (no unintended routing changes)
- Each inlined stage job matches its corresponding `reusable-{stage}.yml` (same steps, permissions, concurrency)
- `uses: reusable-{stage}.yml@v0` lines are gone
- Secrets (`FULLSEND_GCP_WIF_PROVIDER`, `FULLSEND_GCP_PROJECT_ID`) declared in `on.workflow_call.secrets`

### 2. Create prepare-workspace composite action

Add `.github/actions/prepare-workspace/action.yml` to DRY up workspace setup across the six inlined stage jobs.

### 3. Sync check: inlined stages vs reusable-{stage}.yml

For each stage (triage, code, review, fix, retro, prioritize), verify the inlined job in `reusable-dispatch.yml` matches the standalone `reusable-{stage}.yml`:
- Same permissions
- Same concurrency group pattern (agent-scoped)
- Same steps (checkout, prepare-workspace, mint-token, setup-gcp, setup-agent-env, run agent)
- Same stage-specific logic (fix: fork check, eligibility, review body; code: validation, bot identity; review: prior
review; prioritize: no mint, no checkout)

Key difference allowed: inlined jobs use `prepare-workspace` composite action while standalone files have inline bash. The behavior must be equivalent.

### 4. Verify scaffold tests pass

Run `go test ./internal/scaffold/...` — scaffold files are unchanged so tests must still pass.

### 5. Verify workflow lint

Run any workflow linting (`actionlint` or similar) on both `reusable-dispatch.yml` and the restored `reusable-{stage}.yml` files.

## Files changed

| File | Action |
|------|--------|
| `.github/workflows/reusable-dispatch.yml` | Modify (inline stage jobs) |
| `.github/workflows/reusable-{code,fix,review,triage,retro,prioritize}.yml` | Unchanged (kept for per-org) |
| `.github/actions/prepare-workspace/action.yml` | New (composite action) |
| `internal/scaffold/...` | Unchanged (per-org untouched) |

## Verification

1. `go test ./...` — full test suite passes
2. Diff each inlined stage job against its `reusable-{stage}.yml` counterpart to confirm sync
3. Manual review: per-org flow unchanged (shim → dispatch.yml → thin callers → reusable-{stage}.yml)
4. Manual review: per-repo flow works without version skew (shim → reusable-dispatch.yml with inlined stages)
