# Triage Summary

**Title:** Flaky task ordering tests in CI — likely unstable sort with tied keys

## Problem
Five new tests introduced in the recent task ordering PR fail intermittently in CI (sometimes 2 fail, sometimes all 5, sometimes none) but pass 100% of the time on the reporter's local machine. This is blocking releases.

## Root Cause Hypothesis
The new tests sort tasks and assert a specific output order. Go's sort.Slice uses an unstable sort (pdqsort), meaning elements with equal sort keys can appear in any order. If any test constructs tasks with duplicate values in the sort key (e.g., same priority), the expected order is non-deterministic. Locally, consistent memory allocation patterns produce a repeatable ordering that happens to match assertions. In CI, different memory layout, ASLR, or scheduling causes a different — but equally valid — ordering, breaking the assertions.

## Reproduction Steps
  1. Identify the 5 new ordering tests from the recently merged task ordering PR
  2. Examine the test data: look for tasks with identical values in the field(s) being sorted on
  3. Run `go test -count=100 ./path/to/ordering/tests` locally to force re-execution and surface flakiness
  4. If tests pass locally even with -count=100, try with `-race` or on a different machine/container to vary memory layout

## Environment
Go test suite run via `go test ./...`, same command locally and in CI. CI environment details unknown but standard Go CI pipeline.

## Severity: high

## Impact
Blocking all releases. Affects the entire team. No workaround other than re-running CI until tests happen to pass.

## Recommended Fix
1. Inspect the sort comparator in the task ordering code — check if it has a total ordering or allows ties. 2. If ties are possible, either add a tiebreaker to the sort (e.g., sort by priority then by ID/name) or use sort.SliceStable instead of sort.Slice. 3. Fix the test assertions to either account for non-deterministic ordering of equal elements OR ensure test data has no ties in the sort key. 4. Verify the fix by running `go test -count=100` to confirm stability.

## Proposed Test Case
Add a dedicated test that creates tasks with deliberately identical sort keys and verifies the sort produces a deterministic result. Run this test with -count=1000 to confirm no flakiness. This test should fail before the fix and pass consistently after.

## Information Gaps
- Exact field(s) used as sort keys (requires code inspection — reporter didn't write the tests)
- Whether sort.Slice or sort.SliceStable is currently used (requires code inspection)
- Exact CI environment details (OS, Go version, container runtime) — unlikely to matter if the sort hypothesis is correct
- Actual CI failure logs and error messages — would confirm assertion mismatches on ordering
