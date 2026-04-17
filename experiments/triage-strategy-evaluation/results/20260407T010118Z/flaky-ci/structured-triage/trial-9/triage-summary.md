# Triage Summary

**Title:** Flaky task ordering tests in CI due to non-deterministic sort/iteration order (Go, Ubuntu vs macOS)

## Problem
Tests merged 4 days ago in the task ordering module (TestTaskOrderAfterSort, TestInsertionOrderPreserved, TestMultipleTaskOrdering) pass consistently on macOS but fail intermittently on Ubuntu 22.04 in GitHub Actions CI. Assertion errors show all expected elements present but in wrong order.

## Root Cause Hypothesis
The task ordering code relies on non-deterministic iteration order (likely Go map iteration or an unstable sort). On macOS the memory layout or allocator happens to produce a consistent order, masking the bug. On Linux/Ubuntu the ordering varies between runs, exposing the flaky assertions. The PR merged 4 days ago likely introduced code that iterates a map or uses sort.Slice (which is unstable) without a tiebreaker, producing non-deterministic output.

## Reproduction Steps
  1. Check out the branch/commit that includes the task ordering PR merged ~4 days ago
  2. Run the task ordering test suite on Ubuntu 22.04 with Go 1.22 (or in GitHub Actions)
  3. Run repeatedly (may take several runs to see failure): go test -count=10 -run 'TestTaskOrderAfterSort|TestInsertionOrderPreserved|TestMultipleTaskOrdering' ./...
  4. Observe assertion errors like 'expected [task1, task2, task3] got [task2, task3, task1]'

## Environment
CI: GitHub Actions, Ubuntu 22.04, Go 1.22. Local (reporter): macOS, Go 1.22. Unit tests only, no database.

## Severity: high

## Impact
Blocking releases for the entire team. CI pipeline is unreliable, requiring re-runs and eroding trust in the test suite.

## Recommended Fix
Review the task ordering PR for (1) map iteration used to build result slices — replace with sorted key iteration or a slice-based data structure, (2) use of sort.Slice — switch to sort.SliceStable or add a tiebreaker field (e.g., ID) to ensure deterministic ordering when primary sort keys are equal. Also consider running tests locally with -count=100 or setting GOFLAGS=-count=10 to catch such issues before merge.

## Proposed Test Case
Add a test that inserts tasks with identical sort keys (e.g., same priority/timestamp) and asserts the output order is deterministic across 100 runs (go test -count=100). This validates that tiebreaking logic produces stable results regardless of platform.

## Information Gaps
- Exact source file and function in the task ordering PR that produces the non-deterministic order
- Whether the code uses map iteration, unstable sort, or goroutine-based concurrency to build results
