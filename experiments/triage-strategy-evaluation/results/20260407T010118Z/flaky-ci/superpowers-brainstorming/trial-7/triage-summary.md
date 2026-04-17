# Triage Summary

**Title:** Flaky CI: new task-ordering tests assume stable sort behavior

## Problem
A recently merged PR added tests for task sorting/ordering logic. These tests pass consistently on the author's local machine but fail intermittently in CI. The tests assert that tasks come back in insertion order after sorting. Both environments run the same Go version.

## Root Cause Hypothesis
The tests rely on Go's sort.Slice, which is an unstable sort (it does not preserve the original order of equal elements). The test data likely contains tasks with identical sort keys (e.g., same priority or due date). Locally, the underlying memory layout or allocation pattern happens to preserve insertion order for equal elements, making the tests appear stable. In CI, different runner hardware, memory pressure, or goroutine scheduling produces a different — but equally valid — ordering of equal elements, causing assertion failures.

## Reproduction Steps
  1. Identify the PR that added the new task-ordering tests
  2. Examine the test data for tasks that share the same value for the sort key (priority, due date, etc.)
  3. Run the tests repeatedly (e.g., `go test -count=100 ./...`) — on most machines this will eventually surface the failure
  4. Alternatively, reverse the insertion order of equal-key tasks in the test data and run once — the assertions should fail immediately if this is the cause

## Environment
Go (same version locally and in CI). CI runner specifics not identified but not needed — the issue is in the sort contract, not the environment.

## Severity: high

## Impact
Blocking all releases. The entire team is unable to merge and ship because CI is unreliable. The flakiness also erodes trust in the test suite.

## Recommended Fix
Two options depending on intent: (1) If the application requires a stable sort, switch from sort.Slice to sort.SliceStable (or slices.SortStableFunc). (2) If stable ordering of equal elements is not a product requirement, fix the tests — either add a tiebreaker field (e.g., sort by priority then by ID) so no two tasks are 'equal', or relax the assertions to only verify the sort-key ordering without asserting the relative order of equal-key tasks.

## Proposed Test Case
Create test data with at least two tasks that share the same sort key value. Assert only that the sort-key order is correct (task with higher priority comes first, etc.) without asserting anything about the relative order of same-key tasks. Run with -count=100 to confirm no flakiness.

## Information Gaps
- Exact sort key field(s) used and which specific test data entries share values (developer can read the test code directly)
- Whether the application's product requirements demand stable ordering of equal-priority tasks (this would affect fix direction but the developer/PM can decide)
