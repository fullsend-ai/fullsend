# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to non-deterministic sort

## Problem
Tests added in a recent PR assert that tasks are returned in a specific order after sorting. These tests pass consistently on developer machines but fail intermittently in CI, with values appearing in the wrong order.

## Root Cause Hypothesis
The sorting operation used by the task ordering module does not have a fully deterministic tiebreaker. When multiple tasks share the same value for the primary sort key (e.g., same priority or timestamp), their relative order is undefined. The local database engine happens to return them in a stable order (likely insertion order), masking the bug. CI uses a different database instance, configuration, or connection pooling that exposes the non-determinism.

## Reproduction Steps
  1. Identify the failing tests in the task ordering module (added ~4 days ago)
  2. Examine the sort/query they exercise and identify the primary sort key
  3. Create test data where multiple tasks share the same value for that sort key
  4. Run the tests repeatedly (or under a different DB configuration) to observe order instability

## Environment
CI environment (specific configuration unknown, but differs from local in database behavior). Failures are environment-sensitive, not code-path-sensitive.

## Severity: medium

## Impact
Blocks release pipeline due to intermittent CI failures. No production bug — the application behavior is correct, but the test assertions are too strict for a non-deterministic sort.

## Recommended Fix
1. Add a secondary sort key (e.g., task ID or created_at timestamp) to the query/sort operation so ordering is fully deterministic. 2. Alternatively, if the business logic doesn't require a specific order among equal-priority items, relax the test assertions to only verify the primary sort criteria (e.g., check that priorities are non-decreasing rather than asserting exact element positions). 3. Review the PR merged ~4 days ago to find the exact tests and sorting logic involved.

## Proposed Test Case
Create multiple tasks with identical values for the primary sort key, run the ordering query 50+ times in a loop, and assert the results are identical every time. This validates that the tiebreaker produces a stable, deterministic order.

## Information Gaps
- Exact PR number and test file names (easily found from CI logs and recent merge history)
- Which database engine and version CI uses vs. local development
- Whether the sort is happening at the database level (ORDER BY) or application level
