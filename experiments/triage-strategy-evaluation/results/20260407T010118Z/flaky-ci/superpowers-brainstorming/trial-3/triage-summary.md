# Triage Summary

**Title:** Task ordering tests are flaky in CI due to nondeterministic sort (missing tiebreaker)

## Problem
Task ordering tests added last week intermittently fail in CI. The assertions expect tasks in a specific order, but the results sometimes come back in a different order. Tests pass consistently on the reporter's local machine.

## Root Cause Hypothesis
The query (or API endpoint) that returns sorted tasks sorts by a field that has duplicate values across test records (e.g., same priority or same due date). Without a secondary tiebreaker column (like `id` or `created_at`), the database is free to return tied rows in any order. Locally, the reporter likely gets stable insertion-order behavior (common with SQLite or low concurrency), while the CI environment uses a different database engine, parallelism, or page layout that surfaces the nondeterminism.

## Reproduction Steps
  1. Run the task ordering test suite multiple times in the CI environment
  2. Observe that assertions on task order intermittently fail
  3. Inspect the failing assertions — tasks with equal sort-key values appear in different positions across runs

## Environment
CI environment (exact DB engine and runner config TBD — compare against local dev setup which likely uses SQLite or single-threaded execution)

## Severity: high

## Impact
Blocking releases. The entire team is unable to merge and ship due to flaky CI failures.

## Recommended Fix
1. Identify the sort clause in the query exercised by the failing tests (look at the task ordering tests added last week). 2. Add a deterministic tiebreaker to the ORDER BY clause — typically `id` as the final sort key (e.g., `ORDER BY priority, created_at, id`). 3. Verify the same tiebreaker is applied in both the application code and any API layer. 4. Re-run the CI suite multiple times to confirm stability.

## Proposed Test Case
Create multiple tasks with identical values for the primary sort field (e.g., same priority and same created_at). Assert that they are returned in a stable, deterministic order (by id). Run this test 10+ times in CI to confirm no flakiness.

## Information Gaps
- Exact sort field(s) currently used in the query — developer can find this by inspecting the test code and the endpoint/query it calls
- CI database engine vs. local database engine — useful for confirming the hypothesis but doesn't change the fix
- Whether other endpoints have the same missing-tiebreaker issue beyond the tests added last week
