# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to likely non-deterministic sort or CI-specific test flags

## Problem
Tests added in a recent PR (~4 days ago) to the task ordering module fail intermittently in CI with wrong-order assertions, but pass consistently on the developer's local machine (macOS). The failures are random — sometimes all tests pass, sometimes a handful fail. This is blocking releases.

## Root Cause Hypothesis
The task ordering tests assert a specific sort order that is non-deterministic under certain conditions. The most likely cause is one of: (1) CI uses `go test -shuffle` which randomizes test execution order, exposing tests that depend on implicit ordering between test cases or shared state; (2) CI uses higher `-parallel` or `-count` values causing race conditions in shared test fixtures; (3) the sort being tested is unstable (equal-priority items have no tiebreaker), and different environments resolve ties differently. The macOS-vs-Ubuntu difference may also manifest through different default sort behavior or timestamp precision in test data seeding.

## Reproduction Steps
  1. Identify the PR merged ~4 days ago that added ~5 tests to the task ordering module (check recent merge history)
  2. Compare the CI pipeline test command against the local dev config test command — look specifically for flags like -shuffle, -parallel, -race, -count
  3. Run the task ordering tests locally with the exact CI flags (e.g., `go test -shuffle=on -count=5 ./path/to/task/ordering/...`)
  4. If that doesn't reproduce it, also try running on a Linux environment to match CI

## Environment
Local: macOS, Go 1.22, dev config with unknown Go test flags. CI: Ubuntu, Go 1.22, pipeline config with unknown Go test flags.

## Severity: high

## Impact
Blocking all releases for the team. Every CI run is a coin flip, eroding confidence in the test suite. The team is spending significant time re-running pipelines hoping for green builds.

## Recommended Fix
1. Pull up the CI config and local dev config side-by-side — identify differing test flags. 2. Read the failing test code to check whether the sort under test has a stable tiebreaker (e.g., if sorting by priority, what happens when two tasks have the same priority?). 3. If the sort is unstable, add a secondary sort key (e.g., by ID or creation timestamp) to make it deterministic. 4. If CI uses -shuffle and tests depend on execution order or shared state, fix the tests to be independent. 5. Ensure CI and local dev config use the same test flags to prevent future 'works on my machine' divergence.

## Proposed Test Case
Create a test that inserts multiple tasks with identical sort keys (e.g., same priority, same timestamp) and asserts that the returned order is stable across 100 consecutive runs. This would catch unstable sorts that only manifest intermittently.

## Information Gaps
- Exact CI test command and flags (obtainable from pipeline config in repo)
- Exact local dev config test flags (obtainable from dev config file in repo)
- The specific test file and assertions that fail (obtainable from recent merge history ~4 days ago)
- Whether the sort implementation uses a stable sort algorithm or has a tiebreaker
