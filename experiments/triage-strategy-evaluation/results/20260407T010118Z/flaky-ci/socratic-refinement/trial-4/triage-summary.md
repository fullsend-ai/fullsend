# Triage Summary

**Title:** New task ordering tests fail intermittently in CI with ordering mismatches (pass locally)

## Problem
Task ordering tests added approximately one week ago fail randomly in CI with incorrect result ordering (e.g., expected [A, B, C] but got [C, A, B]), while passing 100% of the time on the developer's local Mac. This has been blocking releases for ~4 days.

## Root Cause Hypothesis
The ordering tests rely on a sort or query result order that is not fully deterministic. Most likely causes, in order of probability: (1) An unstable sort on a field with duplicate values — Go's sort.Slice is not stable, so equal elements may appear in any order, and the local Mac environment happens to produce a consistent order while CI does not. (2) A database query missing an explicit ORDER BY clause (or an insufficient tiebreaker column), where the DB engine returns rows in a consistent order locally but not under CI's production-like config. (3) Go map iteration order randomness leaking into results. The dev-config vs production-like-config difference in CI may also affect database engine behavior, connection pooling, or parallelism in ways that surface the non-determinism.

## Reproduction Steps
  1. Check out the branch/commit that introduced the task ordering tests (~1 week ago)
  2. Run `go test -count=10 -shuffle=on ./...` targeting the ordering test package to attempt local reproduction
  3. Alternatively, run tests with the CI (production-like) config locally instead of the dev config
  4. Inspect whether failures show ordering mismatches in the task ordering test assertions

## Environment
CI: Ubuntu, Go 1.22, production-like configuration. Local (reporter): macOS, Go 1.22, dev configuration. The exact CI config differences are unknown to the reporter but are available in the CI pipeline definition.

## Severity: high

## Impact
Blocking all releases for the team for ~4 days. Developers are re-running CI pipelines hoping for green builds, wasting time and reducing confidence in the test suite.

## Recommended Fix
1. Examine the task ordering tests and identify what field(s) tasks are sorted by. 2. If using sort.Slice, switch to sort.SliceStable, or add a tiebreaker (e.g., ID) so the sort is fully deterministic even when primary sort keys are equal. 3. If ordering comes from a database query, ensure the ORDER BY clause includes a unique tiebreaker column (e.g., `ORDER BY priority, created_at, id`). 4. Compare the dev config and CI (production-like) config for differences that could affect ordering (e.g., database engine, parallelism settings, connection pooling). 5. Validate the fix by running `go test -count=20 -shuffle=on` to confirm stability.

## Proposed Test Case
Create a test with multiple tasks that have identical values for the primary sort key (e.g., same priority or same timestamp) and assert that the returned order is deterministic across 50+ iterations. This test should fail before the fix and pass after, confirming the tiebreaker works.

## Information Gaps
- Exact CI error output / assertion messages (reporter didn't have logs available; obtainable from CI system directly)
- Specific differences between dev config and production-like CI config (obtainable from CI pipeline definition)
- Whether CI uses go test -shuffle, -parallel, or -race flags (obtainable from CI config)
- The exact sort key / ORDER BY clause used in the task ordering implementation (obtainable from code)
