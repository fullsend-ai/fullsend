# Triage Summary

**Title:** 5 new tests flaky in CI due to non-deterministic task ordering

## Problem
Five tests added in a recent PR fail intermittently in CI with wrong return order for tasks, while passing consistently on local machines. A random subset of the 5 fails each run, and the failing combination is never the same twice. This has been blocking releases for ~4 days.

## Root Cause Hypothesis
The new tests assert on the order of returned tasks, but the underlying query or data access does not guarantee a deterministic order (likely missing an ORDER BY clause or relying on insertion order). The local development environment happens to return rows in a consistent order (common with SQLite or single-connection setups), masking the bug. CI uses a different database engine, connection pooling, or parallel test execution that surfaces the non-determinism.

## Reproduction Steps
  1. Identify the 5 tests added in the coworker's PR (~4 days ago)
  2. Examine what queries or API calls those tests make to retrieve tasks
  3. Run the test suite repeatedly in CI (or locally against the CI database configuration) to observe intermittent ordering failures
  4. Check whether the tests assert on array order of results without the underlying query specifying ORDER BY

## Environment
CI pipeline (specific CI environment and database engine not yet confirmed); passes on local developer machines

## Severity: high

## Impact
Blocking all releases for the team for ~4 days. All team members affected. No production bug, but development velocity is halted.

## Recommended Fix
1. Inspect the queries exercised by the 5 failing tests for missing ORDER BY clauses — add explicit ordering where the test expectations depend on order. 2. Alternatively, if the tests don't actually care about order, change the assertions to be order-independent (e.g., sort both expected and actual before comparing, or use set-equality assertions). 3. Verify that local dev and CI use the same database engine and configuration to prevent similar masking in the future.

## Proposed Test Case
Run the 5 tests 20+ times in a loop in CI (or against the CI database config locally) and confirm zero failures after the fix. Additionally, add a test that explicitly inserts tasks in reverse order and verifies the query returns them in the expected sorted order.

## Information Gaps
- Exact database engine used locally vs. in CI (e.g., SQLite vs. PostgreSQL)
- Whether CI runs tests in parallel, which could compound the non-determinism
- The specific sort key the tests expect (timestamp, ID, name, etc.)
