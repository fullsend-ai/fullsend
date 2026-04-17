# Triage Summary

**Title:** Flaky ordering assertion failures in CI: new task ordering tests assume deterministic order from non-deterministic source

## Problem
Recently added task ordering unit tests pass consistently on the reporter's Mac but fail intermittently on GitHub Actions Ubuntu runners. Failures are assertion errors where tasks are returned in an unexpected order.

## Root Cause Hypothesis
The new ordering tests likely depend on iteration order of a Go map or another non-deterministic source (goroutine scheduling, etc.) without applying an explicit sort. Go map iteration order is randomized, and behavioral differences between macOS and Linux (or between Go runtime seeds) can make this appear stable locally while failing in CI.

## Reproduction Steps
  1. Push a branch that includes the new ordering tests added last week
  2. Trigger CI pipeline on GitHub Actions (Ubuntu runner, Go 1.22)
  3. Observe that the ordering test suite intermittently fails with assertion errors showing tasks in a different order than expected
  4. Locally, run the same tests with '-count=100' or 'go test -race' to attempt to surface the flakiness

## Environment
CI: GitHub Actions, Ubuntu runners, Go 1.22. Local: macOS, Go 1.22. Unit tests are in-memory with no database dependency.

## Severity: high

## Impact
Blocking releases for the team. Every CI run is a coin flip, forcing repeated retries and eroding confidence in the test suite.

## Recommended Fix
Inspect the new ordering tests added last week. Look for assertions that compare slices or ordered output derived from Go map iteration without an explicit sort. Either (a) sort the results before asserting order, (b) use an order-independent assertion (e.g., ElementsMatch), or (c) ensure the production code applies a deterministic sort before returning results. Also consider running 'go test -count=100' locally to verify the fix eliminates flakiness.

## Proposed Test Case
Run the ordering tests with '-count=1000' on both macOS and Linux (or in CI) and confirm zero failures. Additionally, add a test that explicitly shuffles input before calling the ordering logic to prove the output order is deterministic regardless of input order.

## Information Gaps
- Exact test file and function names for the new ordering tests
- Specific assertion output showing expected vs. actual order
- Whether the flakiness can be reproduced locally with '-count=N' or '-race'
