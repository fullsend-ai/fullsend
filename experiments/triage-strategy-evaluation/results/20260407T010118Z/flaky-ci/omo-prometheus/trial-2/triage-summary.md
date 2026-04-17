# Triage Summary

**Title:** Flaky CI: New task ordering tests assert deterministic sequence on non-deterministic query results

## Problem
Five new tests added to the task ordering module ~4 days ago are intermittently failing in CI with assertion errors. The tests expect items in a specific sequence, but receive the same items in a different order. Tests pass consistently in local development but fail randomly in CI, which uses a different, more production-like database configuration.

## Root Cause Hypothesis
The new ordering tests assert exact sequence equality on query results that lack a fully deterministic sort. The underlying query likely sorts by a field (e.g., timestamp, priority) where ties are possible, without a tiebreaker column (e.g., `id`). The local database engine or config happens to return a stable insertion-order for tied rows, while the CI database does not, exposing the non-determinism.

## Reproduction Steps
  1. Check out main at the commit where the ordering tests were added (~4 days ago)
  2. Run the task ordering test suite against the CI database configuration
  3. Observe intermittent assertion failures where expected and actual arrays contain the same items in different order

## Environment
CI environment with a 'more production-like' database configuration (exact differences unknown to the reporter). Local development environment uses a different config that masks the non-determinism.

## Severity: high

## Impact
Blocks all releases. The entire team is affected — CI failures are intermittent so retrying sometimes works, but the pipeline is unreliable.

## Recommended Fix
1. Find the PR merged ~4 days ago that added ~5 tests to the task ordering module (most recent merge to main around that date). 2. Inspect the test assertions: replace exact sequence assertions (`assertEqual([A,B,C], result)`) with either (a) set/bag equality where order doesn't matter, or (b) add a deterministic tiebreaker (e.g., `ORDER BY priority, id`) to the underlying query so the order is guaranteed. 3. Audit the application query itself — if the UI or API depends on this ordering, the tiebreaker should be added to the production query, not just the tests.

## Proposed Test Case
Create a test scenario where multiple tasks share the same sort-key value (e.g., same priority or same timestamp), then assert that the query returns them in a fully deterministic order (by tiebreaker). Run this test 20+ times in a loop to confirm stability.

## Information Gaps
- Exact PR number (findable from merge history — no reporter action needed)
- Specific CI database engine and config differences vs local (inspectable from CI config files)
- Whether the non-deterministic order also affects production behavior or only tests
