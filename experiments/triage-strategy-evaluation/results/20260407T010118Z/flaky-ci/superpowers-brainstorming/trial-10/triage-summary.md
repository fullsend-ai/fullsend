# Triage Summary

**Title:** Task ordering tests from last week's PR are flaky in CI (pass locally)

## Problem
Five tests in the task ordering module, all introduced in a PR last week, fail intermittently in CI while passing consistently on the reporter's local machine. The failures are not random across the suite — it is always the same 5 tests, and on any given run they either all pass or a subset fails. This is blocking releases.

## Root Cause Hypothesis
The task ordering tests likely depend on something that differs between the local environment and CI — most probably timing or execution speed. Since it's always the same 5 tests (not different tests each run), this is not a test-ordering/shared-state issue. The consistent-locally, flaky-in-CI pattern strongly suggests race conditions or timing-sensitive assertions that happen to always win locally but sometimes lose on CI runners with different performance characteristics or resource contention.

## Reproduction Steps
  1. Pull up the last 3-5 CI runs for the main branch or recent PRs
  2. Identify the 5 failing tests in the task ordering module
  3. Examine those tests for timing-sensitive patterns: sleeps, polling loops, async awaits with timeouts, ordering assumptions on concurrent operations
  4. Attempt to reproduce by running those tests under resource pressure (e.g., CPU throttling, parallel test execution, or reduced timeouts)

## Environment
CI environment (specific runner details to be confirmed from CI config). Failures do not reproduce on the reporter's local machine.

## Severity: high

## Impact
Blocking releases for the team. Every CI run is a coin flip, eroding confidence in the test suite.

## Recommended Fix
1. Pull up recent CI logs and identify the exact 5 failing tests. 2. Review the test code from last week's PR for race conditions, timing assumptions, or missing awaits. 3. Look for non-deterministic ordering assumptions (e.g., asserting array order on results that aren't guaranteed to be sorted). 4. If the tests create/reorder tasks concurrently, ensure proper synchronization or deterministic assertions. 5. Consider running the tests with a CI-like configuration locally (parallel execution, resource limits) to reproduce.

## Proposed Test Case
After fixing the flaky tests, run them in a loop (e.g., 50-100 iterations) under parallel execution to confirm the flakiness is resolved. The tests should pass consistently under CI-equivalent conditions.

## Information Gaps
- Exact names of the 5 failing tests (available in CI logs)
- CI runner configuration and parallelism settings
- Whether the PR that introduced these tests also changed any application code or just added tests
- Specific error messages or assertion failures from the CI logs
