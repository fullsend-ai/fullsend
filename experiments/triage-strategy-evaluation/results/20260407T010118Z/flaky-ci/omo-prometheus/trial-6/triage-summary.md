# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to non-deterministic map iteration in Go

## Problem
Five new tests introduced in a recent PR (merged ~4 days ago) that test task ordering logic fail intermittently in CI with assertion errors — the expected task order does not match the actual output. Tests pass consistently on the reporter's local machine.

## Root Cause Hypothesis
The task ordering code groups tasks by category using a Go map (e.g., map[string][]Task). Go intentionally randomizes map iteration order at runtime. When the code iterates over this map to build the final ordered result, the order of category groups is non-deterministic, producing different task orderings across runs. The tests assert a specific order but the code does not guarantee it. Local consistency is likely coincidental — Go's map randomization varies by runtime conditions, and with fewer cores or different scheduling, the local machine may happen to produce the same order repeatedly.

## Reproduction Steps
  1. Check out the branch/commit with the recent task ordering PR merged ~4 days ago
  2. Run the 5 new task ordering tests repeatedly with: go test -count=20 -run <test_pattern> ./path/to/ordering/package
  3. Observe that some runs produce assertion failures where task order differs from expected
  4. Alternatively, run with -race flag as the reporter does: go test -race -count=20 -run <test_pattern>

## Environment
Go application, same database and setup in local and CI. Reporter runs tests with -race flag locally. CI environment details not specified but the issue is language-runtime-level, not environment-specific.

## Severity: high

## Impact
Blocking releases. All team members affected — CI pipeline is unreliable, requiring re-runs and eroding confidence in the test suite.

## Recommended Fix
1. Find the task ordering function from the recent PR that uses a map to group tasks by category. 2. After grouping, sort the map keys (category names) explicitly before iterating: collect keys into a slice, sort the slice, then iterate in that sorted order. 3. If tasks within a category can have equal sort keys, ensure the sort is stable (use sort.SliceStable) or add a tiebreaker (e.g., by task ID) to guarantee deterministic ordering. 4. Verify the tests encode the correct expected order matching the now-deterministic sort.

## Proposed Test Case
Run the 5 existing failing tests with -count=100 to confirm they pass consistently after the fix. Additionally, add a test that creates tasks across multiple categories with identical sort keys and asserts that the output order is deterministic across 50+ iterations (using a loop within the test).

## Information Gaps
- Exact function and file name containing the map-based grouping (easily found from the PR diff)
- Exact names of the 5 failing tests (easily found from CI logs or the PR)
- Whether sort.Slice or sort.SliceStable is used for the within-group sorting
