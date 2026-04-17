# Triage Summary

**Title:** Flaky task ordering tests in CI: non-deterministic assertion failures in recently added ordering tests

## Problem
Tests added in a recent PR to the task ordering module intermittently fail in CI with assertions about incorrect ordering, while consistently passing on the developer's local machine. This has been blocking releases for several days.

## Root Cause Hypothesis
The new ordering tests likely depend on a deterministic iteration or execution order that is not guaranteed. Go 1.22 randomizes subtest execution by default and map iteration order is always randomized. The tests may be asserting on slice/map output order without sorting, or they may depend on shared mutable state (e.g., database rows, in-memory collections) whose order varies by platform or parallelism. The Ubuntu (CI) vs macOS (local) difference may also surface different goroutine scheduling or sort stability behavior.

## Reproduction Steps
  1. Identify the task ordering module tests added in the recent PR
  2. Run the tests with Go's race detector enabled: go test -race ./path/to/ordering/...
  3. Run tests with explicit count to surface flakiness: go test -count=100 ./path/to/ordering/...
  4. Run tests with shuffled order: go test -shuffle=on ./path/to/ordering/...
  5. Compare CI test runner configuration (parallelism, flags) with local defaults

## Environment
Go 1.22, CI runs on Ubuntu, developer runs on macOS. Both using same Go version.

## Severity: high

## Impact
Blocking the team's release pipeline for several days. All team members affected as CI failures prevent merging and shipping.

## Recommended Fix
Review the recently added task ordering tests for order-dependent assertions. Likely fixes: (1) Sort results before asserting order, or use an order-independent assertion (e.g., ElementsMatch instead of Equal). (2) Check for shared test state — ensure each test sets up and tears down its own data. (3) If tests use maps or query results without ORDER BY, add deterministic ordering. (4) Run with -race to rule out a data race causing non-deterministic state.

## Proposed Test Case
Add a test that runs the ordering logic N times (e.g., 100 iterations via -count=100 or a loop) and asserts consistent output, confirming the fix eliminates non-determinism. Additionally, run with -shuffle=on to verify tests are independent of execution order.

## Information Gaps
- Exact error messages and stack traces from a failed CI run
- Whether CI runs tests in parallel or with specific flags not used locally
- Whether the ordering tests use any shared database or in-memory state across test cases
