# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to unstable sort on equal-priority items

## Problem
Unit tests for task ordering logic, introduced in a recent PR, assert a specific output order for tasks that share the same sort key (e.g., same priority). The tests pass consistently on the developer's macOS machine but fail intermittently on the Ubuntu-based CI runner.

## Root Cause Hypothesis
Go's sort.Slice uses an unstable sorting algorithm (pdqsort), meaning the relative order of elements with equal sort keys is not guaranteed and may vary across runs, platforms, and even between invocations on the same machine. The tests assume input-order preservation for equal elements, which happens to hold reliably on the developer's macOS environment due to differences in memory layout or allocator behavior, but breaks non-deterministically on Ubuntu in CI.

## Reproduction Steps
  1. Identify the task ordering tests added in the recent PR
  2. Find test cases where multiple tasks share the same sort key (priority, due date, etc.)
  3. Run those tests with Go's race detector or with -count=100 to surface the non-determinism: `go test -run TestTaskOrdering -count=100 ./...`
  4. Alternatively, run on a Linux environment to reproduce more readily

## Environment
CI: Ubuntu (Linux), Developer local: macOS. Same Go version. Tests are Go unit tests with no database dependency.

## Severity: high

## Impact
Blocking releases for multiple days. All team members affected. No workaround currently in use other than re-running CI and hoping for a pass.

## Recommended Fix
Two options (apply one or both): (1) Replace sort.Slice with sort.SliceStable if preserving input order for equal elements is the intended behavior. (2) Fix the test assertions so they don't depend on the relative order of items with equal sort keys — either by using a comparison that treats equal-key permutations as equivalent, or by adding a tiebreaker field (e.g., creation timestamp or ID) to the sort comparator so the order is fully deterministic.

## Proposed Test Case
Add a dedicated test with multiple tasks sharing the same sort key, run it with -count=1000, and assert that results are consistent across all iterations. This validates that the sort is deterministic regardless of platform.

## Information Gaps
- Exact PR number and test file path (developer can locate via recent merge history)
- Whether the intended product behavior is to preserve insertion order for equal-priority tasks or if any stable ordering is acceptable — this affects whether the fix should be sort.SliceStable vs. adding a tiebreaker
