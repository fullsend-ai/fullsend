# Triage Summary

**Title:** Task ordering tests flaky in CI due to likely unstable sort with equal keys

## Problem
New task ordering tests added ~4 days ago intermittently fail in CI (Ubuntu) while always passing locally (macOS). The failures are order-assertion mismatches — tasks come back in an unexpected order after sorting — not timeouts or missing data.

## Root Cause Hypothesis
Go's sort.Slice is an unstable sort. If any tasks in the test fixtures share the same value for the sort key (e.g., same priority, same timestamp), their relative order is non-deterministic. The tests likely assert a specific exact ordering that depends on the initial input order being preserved for equal elements, which is not guaranteed. Differences between macOS and Linux memory allocators change the input arrangement, making the flakiness more visible on CI's Ubuntu environment.

## Reproduction Steps
  1. Examine the task ordering test fixtures for tasks that share the same sort-key value
  2. Run the ordering tests in a loop on Linux: `go test -count=100 -run TestTaskOrdering ./...`
  3. Observe intermittent failures where equal-keyed tasks appear in different relative orders

## Environment
CI: Ubuntu, Go 1.22. Local: macOS, Go 1.22. Both using same Go version; difference is OS and memory layout.

## Severity: medium

## Impact
Blocking CI pipeline and releases for the team. No production impact — the sorting logic itself may be correct, but the tests are brittle.

## Recommended Fix
1. Inspect the sort comparator in the task ordering module for a missing tiebreaker. Add a deterministic secondary sort key (e.g., task ID) so that equal elements always resolve to the same order. 2. Alternatively, if the test expectations are too strict, use sort.SliceStable instead — but only if preserving insertion order for equal elements is the intended behavior. 3. Update test assertions to either account for the tiebreaker or use an order-agnostic comparison for elements with equal sort keys.

## Proposed Test Case
Add a test with multiple tasks sharing the same primary sort key and assert that the output is ordered by the tiebreaker field (e.g., task ID). Run it with `-count=1000` to verify stability across many iterations on both macOS and Linux.

## Information Gaps
- Exact sort key(s) used in the comparator (confirming which field lacks a tiebreaker)
- Whether sort.Slice or sort.SliceStable is currently used
- Specific test fixture data (to confirm equal keys exist)
