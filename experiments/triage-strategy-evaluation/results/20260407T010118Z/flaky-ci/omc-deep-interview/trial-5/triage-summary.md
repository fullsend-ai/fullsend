# Triage Summary

**Title:** Intermittent CI failures in task ordering tests due to unstable sort on equal-priority items

## Problem
Tests in the task ordering module fail intermittently in CI (Ubuntu) but pass consistently in local development (macOS). Failures report unexpected ordering of tasks after sorting. The tests were introduced in a recent PR and assert that tasks are returned in a specific order after sorting by priority.

## Root Cause Hypothesis
The sorting implementation likely uses Go's sort.Slice, which is not a stable sort. When multiple tasks share the same priority, their relative order is non-deterministic. The tests assert insertion order is preserved for equal-priority items, which happens to hold locally due to consistent memory layout but fails under different conditions in CI. The macOS-vs-Linux difference in memory allocation patterns makes the non-determinism more visible in CI.

## Reproduction Steps
  1. Identify the task ordering test file added in the recent PR (in the task ordering module)
  2. Run `go test -count=100 ./path/to/task/ordering/...` locally to surface the flake
  3. Inspect the test assertions for cases where multiple tasks share the same sort key (e.g., same priority)
  4. Confirm that the sort implementation uses sort.Slice (unstable) rather than sort.SliceStable

## Environment
CI: Ubuntu, Go 1.22. Local dev: macOS, Go 1.22. Test runner: `go test` (default settings, no explicit parallelism).

## Severity: high

## Impact
Blocking releases for the team. Every CI run is a coin flip, eroding confidence in the test suite and slowing development velocity.

## Recommended Fix
Either (a) switch from sort.Slice to sort.SliceStable to preserve insertion order for equal-priority items, or (b) add a deterministic tiebreaker to the sort comparison (e.g., sort by priority then by task ID or creation timestamp), or (c) fix the test assertions to not depend on the ordering of equal-priority items (e.g., only assert that items are grouped by priority, or sort the equal-priority subset before comparing). Option (b) is usually the best long-term choice since it makes the API behavior deterministic for users too, not just for tests.

## Proposed Test Case
Create a test with multiple tasks that share the same priority. Run it with -count=1000 and assert that the output order is deterministic. This validates that the sort is either stable or uses a tiebreaker, preventing future flakes.

## Information Gaps
- Exact test file path and test function names (discoverable from CI logs or the recent PR)
- Whether go test -count=100 reproduces the failure locally (strongly expected to, but unconfirmed)
- Whether any other CI config differences (env vars, test flags) contribute beyond the sort instability
