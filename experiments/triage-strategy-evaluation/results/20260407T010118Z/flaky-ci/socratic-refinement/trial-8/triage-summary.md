# Triage Summary

**Title:** New tests from recent PR fail intermittently in CI due to non-deterministic task ordering

## Problem
Tests added in a recent PR assert that tasks are returned in a specific order. In CI, the order is sometimes different, causing assertion failures. Locally, the tests always pass — likely because the local environment happens to return tasks in a consistent order.

## Root Cause Hypothesis
The new tests assume deterministic ordering of task results, but the underlying query or data source does not guarantee order. The local environment (e.g., SQLite with implicit insertion-order, or single-threaded execution) masks this by coincidentally returning results in the expected order, while CI (e.g., PostgreSQL, parallel workers, or different data seeding) exposes the non-determinism.

## Reproduction Steps
  1. Identify the failing test names from CI logs (reporter confirms they are visible there)
  2. Examine the assertions in those tests — look for exact-order comparisons of task lists
  3. Check the query or API call that produces the task list — confirm it lacks an explicit ORDER BY or sort
  4. Run the tests repeatedly in CI or with a shuffled/randomized DB to reproduce locally

## Environment
CI pipeline (specific runner/DB configuration unknown but differs from local). Local environment passes consistently, suggesting it implicitly preserves insertion order.

## Severity: high

## Impact
Blocking releases for the team. CI pipeline is unreliable, forcing re-runs and eroding confidence in the test suite.

## Recommended Fix
Two-pronged fix: (1) If the application should return tasks in a defined order, add an explicit ORDER BY / sort to the relevant query or API endpoint, and the tests are correct to assert order. (2) If the order is not meaningful, update the new tests to use order-independent assertions (e.g., sort both lists before comparing, or use set-based comparison). Also review whether the PR that introduced the tests intended a specific ordering contract — if so, the production code needs the fix, not the tests.

## Proposed Test Case
A test that inserts tasks in a randomized order (or runs with a database that does not preserve insertion order) and verifies the API returns them in the expected sorted order — confirming the ordering contract is enforced by the application, not by accident.

## Information Gaps
- Exact CI environment (database engine, test parallelism settings) vs. local — would confirm the mechanism but not change the fix
- Specific PR number and failing test names — developer can find these in CI logs and git history
- Whether the intended behavior is ordered or unordered task retrieval — developer should determine this from product requirements before choosing fix direction
