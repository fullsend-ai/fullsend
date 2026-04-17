# Triage Summary

**Title:** TestTaskOrder tests fail intermittently in CI with order assertion mismatches (pass locally on Mac)

## Problem
Five TestTaskOrder tests introduced in a recent task ordering PR fail intermittently in GitHub Actions CI with assertion errors indicating the returned order doesn't match expectations. The same tests pass consistently on the reporter's local Mac with the same Go version (1.22).

## Root Cause Hypothesis
The task ordering implementation likely relies on non-deterministic ordering that happens to be stable on macOS but not on Linux. Most probable causes: (1) iterating over a Go map to build the ordered result — map iteration order is randomized per-spec but can appear stable on one platform; (2) using sort.Sort or slices.Sort on elements with equal keys — Go's sort is not stable, and tie-breaking may differ across platforms; (3) database or filesystem ordering differences between macOS and Ubuntu.

## Reproduction Steps
  1. Check out the branch containing the task ordering PR merge
  2. Run `go test -count=100 -run TestTaskOrder ./...` on a Linux machine or in a GitHub Actions runner
  3. Observe intermittent assertion failures where the returned task order doesn't match the expected order
  4. Compare with running the same command on macOS where it likely passes consistently

## Environment
CI: GitHub Actions, Ubuntu (exact version not specified), Go 1.22. Local: macOS, Go 1.22. Test runner: `go test` with default settings.

## Severity: high

## Impact
Blocking all releases. The entire team cannot merge or ship because CI is unreliable. Flaky tests also erode trust in the test suite.

## Recommended Fix
Examine the TestTaskOrder tests and the task ordering implementation from the recent PR. Look for: (1) map iteration used to produce ordered output — replace with sorted slice or ordered data structure; (2) unstable sort on items with equal sort keys — switch to slices.SortStableFunc or add a deterministic tiebreaker (e.g., sort by ID when priority is equal); (3) any reliance on insertion order from a database query without an explicit ORDER BY clause. Running `go test -count=100 -run TestTaskOrder` locally should reproduce the issue on Linux even if Mac appears stable.

## Proposed Test Case
Add a test that creates tasks with identical sort keys (e.g., same priority, same creation time) and asserts that the output order is deterministic across 100 runs using `go test -count=100`. Alternatively, assert that the output is sorted by the documented ordering criteria rather than comparing against a hardcoded expected sequence.

## Information Gaps
- Exact Ubuntu runner version and whether it recently changed
- Whether the CI config has any test caching or parallelism flags not visible to the reporter
- Exact assertion error text from CI logs (reporter described but did not paste)
