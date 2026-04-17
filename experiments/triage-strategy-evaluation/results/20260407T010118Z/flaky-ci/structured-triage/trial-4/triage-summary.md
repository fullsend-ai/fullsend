# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to non-deterministic ordering

## Problem
Task ordering unit tests introduced ~4 days ago fail intermittently in CI (Ubuntu 22.04 / Go 1.22) with assertion errors indicating incorrect order, but pass consistently on the developer's local machine (macOS 14 / Go 1.22). The failures are blocking releases.

## Root Cause Hypothesis
The tests likely depend on non-deterministic iteration order — most probably Go map iteration order or an unstable sort (sort.Slice is not stable). Go randomizes map iteration order at runtime, and the specific memory layout or hash seed differences between Linux and macOS can make one platform appear deterministic while the other exposes the randomness. The recently merged PR likely introduced code that iterates a map or uses sort.Slice to produce task ordering, and the tests assert on a specific order without accounting for ties or equivalent keys.

## Reproduction Steps
  1. Identify the task ordering tests added in the PR merged ~4 days ago
  2. Run those tests repeatedly in a Linux environment (e.g., Ubuntu 22.04 container): `go test -count=100 -run TestTaskOrder ./...`
  3. Observe intermittent assertion failures on task order

## Environment
CI: Ubuntu 22.04, Go 1.22. Local (passing): macOS 14, Go 1.22. Unit tests only — no database, no parallel test execution.

## Severity: high

## Impact
Blocking releases for the team. All developers merging to the main branch are affected by intermittent CI failures.

## Recommended Fix
Review the task ordering code from the recent PR for: (1) map iteration used to build ordered output — replace with sorted slice of keys; (2) sort.Slice used on items with equal sort keys — switch to sort.SliceStable or add a tiebreaker (e.g., by ID); (3) test assertions that compare slice order — either sort the output deterministically before asserting, or use an order-independent comparison where order doesn't matter. Running `go test -count=100` locally will confirm the fix eliminates flakiness.

## Proposed Test Case
Add a test that creates tasks with identical sort keys (e.g., same priority/timestamp) and verifies the output is in a deterministic order (e.g., secondary sort by ID). Run with `-count=100` to confirm stability.

## Information Gaps
- Exact test file and function names (can be found by reviewing the recent PR)
- Exact assertion error messages from a failed CI run (would confirm the specific ordering discrepancy)
