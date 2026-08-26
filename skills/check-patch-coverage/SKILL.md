---
name: check-patch-coverage
description: >-
  Verify approximate Go patch coverage meets the repo's 80% Codecov threshold.
  Use after writing or updating Go production code — before committing — to
  catch coverage gaps that would fail the codecov/patch status check.
---

# Check Patch Coverage

Verify that new or changed Go production code meets the **80% patch
coverage** threshold configured in [`.codecov.yml`](../../.codecov.yml)
before committing. This prevents `codecov/patch` failures on first push.

## When to use

- After implementing or modifying Go production code under `cmd/` or
  `internal/` (files that are **not** `_test.go`).
- After adding tests for new code — to confirm coverage is sufficient.
- When the fix agent is addressing a coverage-related failure.

## When to skip

- Test-only changes (no production `.go` files modified).
- Documentation, config, or generated-code-only changes.
- Files listed in `.codecov.yml` `ignore:` — these are excluded from
  coverage enforcement.

## Procedure

### 1. Identify changed production files

Determine which non-test Go files you changed relative to the target
branch. **Stage new files first** (`git add`) — `git diff --name-only`
only sees tracked or staged files, so an unstaged new file would be
invisible and the check would silently skip it.

```bash
CHANGED_GO=$(git diff --name-only main -- '*.go' | grep -v '_test.go')
echo "$CHANGED_GO"
```

If the list is empty, patch coverage does not apply — stop here.

### 2. Determine affected packages

```bash
PKGS=$(echo "$CHANGED_GO" | xargs -I{} dirname {} | sort -u | sed 's|^|./|')
echo "$PKGS"
```

### 3. Check for packages with no test files

Before running coverage, check whether each affected package actually
contains test files. Go's `go test -coverprofile` only attributes
coverage to the package under test — a package with zero `_test.go`
files produces no coverage data at all, and Codecov will report 0%
for every changed line.

```bash
MISSING_TESTS=""
for pkg_dir in $PKGS; do
  dir="${pkg_dir#./}"
  if ! ls "$dir"/*_test.go >/dev/null 2>&1; then
    echo "⚠  Package $dir has no test files — codecov/patch will report 0% coverage."
    echo "   Create a _test.go file with direct unit tests for new/modified exported functions."
    MISSING_TESTS="$MISSING_TESTS $dir"
  fi
done
```

If any packages were flagged: **stop and create test files** in those
packages before continuing. The 80% threshold cannot be met when no
coverage profile is generated for a package. Treat this as a coverage
gap regardless of the threshold — add at least one `_test.go` file
with direct unit tests for the new or modified exported functions,
then re-run from step 1.

### 4. Run tests with a cover profile

```bash
go test -coverprofile=coverage.out -count=1 $PKGS
```

If tests fail, fix them first — coverage is meaningless on broken code.

### 5. Check per-function coverage on changed files

For each changed file, inspect coverage:

```bash
for f in $CHANGED_GO; do
  echo "=== $f ==="
  go tool cover -func=coverage.out | grep "$f" \
    || echo "⚠  No coverage data for $f — this file's package may lack test files. See step 3."
done
```

If any file shows no coverage data, go back to step 3 and verify
the package has test files. A `(no coverage data)` result means
Codecov will report 0% for that file's changed lines.

Each output line shows `file:line: function  coverage%`.

### 6. Assess against the 80% threshold

Look at the functions you added or modified:

- **All functions ≥ 80%:** Coverage is sufficient. Proceed to commit.
- **Some functions below 80%:** Add or extend `_test.go` files to cover
  the missing lines. Focus on:
  - New functions you added (these must be tested)
  - Modified functions where you added new branches or error paths
  - Functions at 0% that contain logic (not just simple getters/setters)

After adding tests, re-run from step 4 and re-check.

### 7. Visual inspection (optional, for complex cases)

If function-level coverage is borderline or the function has complex
branching:

```bash
go tool cover -html=coverage.out
```

This opens an HTML view showing exactly which lines are covered (green)
and which are not (red). Use this to target your test additions.

## Understanding the approximation

This procedure approximates Codecov's **line-level patch coverage**
using Go's **function-level coverage** (`go tool cover -func`). The
local check is coarser — Codecov counts individual lines in the diff,
while `go tool cover -func` reports per-function percentages. Aim for
**≥ 80%** on touched functions to stay above the threshold with margin.

The configured tolerance is **5%** (from `.codecov.yml`), so Codecov
will pass at 75% in practice. But targeting 80% locally accounts for
the approximation gap between function-level and line-level metrics.

## Thresholds reference

From [`.codecov.yml`](../../.codecov.yml):

- **Patch coverage target:** 80% (5% tolerance)
- **Project coverage:** must not drop more than 1% below base branch
