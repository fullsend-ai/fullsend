# Triage Summary

**Title:** Flaky CI: Task ordering tests fail intermittently due to non-deterministic sort results

## Problem
Task ordering tests added ~4 days ago pass locally but fail intermittently in CI. The failures show tasks returning in an unexpected order after a sort operation. The test assertions compare against a hardcoded expected sequence.

## Root Cause Hypothesis
The sort operation in the task ordering module does not produce a stable, fully-deterministic order — likely because multiple tasks share the same sort key value (e.g., identical priority, due date, or creation timestamp). Locally the tiebreaker happens to be consistent (e.g., insertion order preserved by the local DB engine), but CI uses a different environment (potentially parallel test execution, different DB engine or configuration, or different data seeding timing) where the tiebreaker differs. The tests assume a single correct ordering that isn't guaranteed by the sort contract.

## Reproduction Steps
  1. Check out the PR merged ~4 days ago that added task ordering tests
  2. Examine the test assertions — they use hardcoded expected sequences
  3. Run the test suite repeatedly (or in a CI-like environment) to observe intermittent ordering differences
  4. Inspect the sort implementation in the task ordering module for missing tiebreaker / unstable sort on equal keys

## Environment
CI environment (specific runner/DB details not provided but the discrepancy is CI vs. local development machine)

## Severity: high

## Impact
Blocking all releases for the team. Every CI run has a chance of spurious failure, requiring reruns and eroding confidence in the test suite.

## Recommended Fix
1. Add a deterministic tiebreaker to the sort operation (e.g., secondary sort by unique ID) so the order is fully specified. 2. Update the test assertions to either reflect the corrected deterministic order or assert the sort contract (e.g., verify relative ordering by sort key rather than exact sequence). 3. If the sort is intentionally unstable, tests should only assert that elements are correctly grouped/ordered by the primary key, not assert a single hardcoded permutation.

## Proposed Test Case
Create multiple tasks with identical sort key values (e.g., same priority and due date), run the sort operation, and assert that the result is ordered correctly by the primary key with a deterministic tiebreaker. Run this test in a loop (e.g., 50 iterations) to verify it never flakes.

## Information Gaps
- Exact sort key(s) used in the task ordering module (priority, due date, creation time, etc.)
- Whether CI uses a different database engine or configuration than local development
- Whether CI runs tests in parallel, which could affect data seeding order
