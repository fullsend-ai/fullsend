# Triage Summary

**Title:** Task ordering tests from recent PR fail intermittently in CI due to non-deterministic sort

## Problem
Task ordering tests introduced in a PR merged last week fail intermittently in CI with assertion errors (expected vs. actual ordering mismatches), but pass consistently when run locally. The failures are limited to this specific set of tests and are blocking releases.

## Root Cause Hypothesis
The task ordering tests rely on a sort that is non-deterministic when elements have equal sort keys. Locally, the sort happens to be stable (producing the expected order every time), but in CI, differences in runtime, memory layout, or parallel test execution cause the unstable sort to produce a different-but-equally-valid order. This is a classic unstable-sort or missing-tiebreaker bug.

## Reproduction Steps
  1. Identify the task ordering tests added in the recent PR (visible in CI failure logs)
  2. Run the test suite multiple times in CI or with CI-equivalent settings (e.g., parallel execution, clean state)
  3. Observe that the ordering assertions fail when tasks with equal sort keys are reordered

## Environment
CI environment (specific platform not identified); failures do not reproduce locally, suggesting a difference in sort stability, parallelism, or initial state between local and CI

## Severity: high

## Impact
Blocking releases for the team. The team is currently working around it by re-running the pipeline, which is unreliable and wastes CI resources.

## Recommended Fix
Review the task ordering logic in the recent PR. Look for sorts without a tiebreaker — if tasks can have equal sort keys (e.g., same priority, same date), add a stable secondary sort key (e.g., task ID or creation timestamp). Then update the tests to either assert using the stable sort or to be order-insensitive where ordering is not guaranteed.

## Proposed Test Case
Create a test with multiple tasks that have identical values for the primary sort key, and verify they are returned in a consistent, well-defined order (by the tiebreaker). Run this test repeatedly (or in a loop) to confirm stability.

## Information Gaps
- Exact PR and test file names (findable from CI logs and recent merge history)
- Whether CI uses parallel test execution vs. local sequential (may explain why local always passes but doesn't change the fix)
