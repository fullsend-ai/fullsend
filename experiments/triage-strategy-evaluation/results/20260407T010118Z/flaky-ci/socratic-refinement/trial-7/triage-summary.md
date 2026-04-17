# Triage Summary

**Title:** New task ordering tests fail intermittently in CI due to non-deterministic query result order

## Problem
Tests added in a recent PR for task ordering produce assertion failures in CI where the returned tasks appear in an unexpected order. Values are correct but sequence differs from expectations. Tests pass consistently on the developer's local machine.

## Root Cause Hypothesis
The new task ordering tests rely on implicit result ordering (e.g., insertion order or default database behavior) that happens to be stable in the local development environment but is not guaranteed in CI. This is likely due to differences between local and CI database engines (e.g., SQLite vs PostgreSQL), database configuration, connection pooling, or test parallelism. Without an explicit ORDER BY clause, relational databases make no ordering guarantees, and the observed order can vary between runs.

## Reproduction Steps
  1. Identify the test files added in the recent task ordering PR
  2. Run the test suite multiple times in the CI environment (or with the CI database engine locally)
  3. Observe that the task ordering tests intermittently fail with assertion errors showing correct values in wrong order
  4. Compare the queries under test for explicit ORDER BY clauses

## Environment
CI pipeline environment; specific CI platform and database engine not confirmed but the local-vs-CI discrepancy is the key factor

## Severity: high

## Impact
Blocking releases for the team. All team members affected. The flaky failures erode CI trust and slow down the entire development workflow.

## Recommended Fix
1. Examine the queries used by the task ordering feature — ensure they include explicit ORDER BY clauses for deterministic results. 2. Examine the test assertions — if the tests are verifying set membership rather than order, use order-insensitive assertions (e.g., compare sorted lists or use set equality). 3. If the tests ARE verifying ordering behavior, ensure the test data has distinct sort keys and the query specifies the intended ORDER BY. 4. Investigate local vs CI database engine differences (e.g., SQLite locally, PostgreSQL in CI) and consider aligning them.

## Proposed Test Case
A test that inserts tasks with deliberately randomized or identical timestamps, queries them through the ordering endpoint, and asserts that results come back in the explicitly specified sort order. Run this test at least 10 times in a loop to confirm determinism.

## Information Gaps
- Exact database engines used locally vs in CI (does not change fix direction)
- Whether CI runs tests in parallel, which could contribute to data interference (unlikely given values are correct, only order differs)
- The specific ORDER BY or lack thereof in the relevant queries (developer can inspect directly)
