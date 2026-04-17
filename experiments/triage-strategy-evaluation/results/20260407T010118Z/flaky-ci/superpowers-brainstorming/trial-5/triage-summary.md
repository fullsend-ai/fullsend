# Triage Summary

**Title:** New task-ordering tests fail intermittently in CI due to unstable sort on equal-priority tasks

## Problem
Unit tests added ~4 days ago assert the exact position of every task after sorting, but some tasks share the same priority value. The sort order among equal-priority tasks is non-deterministic, so the assertions pass on the reporter's local machine (where memory layout happens to produce a consistent order) but fail randomly in CI (where the environment produces a different order).

## Root Cause Hypothesis
The sort implementation used for task ordering does not guarantee a stable, deterministic order for tasks with equal sort keys. The tests assume insertion order is preserved for ties, but this is not guaranteed across environments, runtimes, or sort algorithm implementations.

## Reproduction Steps
  1. Identify the new task-ordering tests added in the PR merged ~4 days ago
  2. Find test cases where multiple tasks share the same priority/sort value
  3. Note that assertions check exact positions of those equal-priority tasks
  4. Run the test suite repeatedly (or shuffle test data input order) to observe non-deterministic ordering of tied elements

## Environment
Fails in CI; passes locally on reporter's machine. Specific CI runner details not gathered but are not relevant — the issue is algorithmic, not environmental.

## Severity: medium

## Impact
Blocks CI pipeline and releases. No production bug — this is a test correctness issue only.

## Recommended Fix
Two options (apply one or both): (1) Add a secondary tiebreaker to the sort (e.g., sort by priority, then by task ID or creation timestamp) so the order is fully deterministic. (2) Update the test assertions to only verify relative ordering of tasks with *different* priorities, not the exact positions of tasks with equal priority. Option 1 is preferred because deterministic ordering is also better for users.

## Proposed Test Case
Create a test with multiple tasks sharing the same priority and assert only that all higher-priority tasks appear before lower-priority ones. If a tiebreaker is added, also test that equal-priority tasks sort by the tiebreaker field. Run the test 50+ times in a loop to confirm no flakiness.

## Information Gaps
- Exact programming language and sort function used (does not change the fix direction)
- Specific test file and function names (developer can find these from the PR merged ~4 days ago)
