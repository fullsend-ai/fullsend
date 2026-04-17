# Triage Summary

**Title:** Flaky task ordering tests in CI — pass locally on macOS, fail intermittently on GitHub Actions (Ubuntu)

## Problem
Tests in the task ordering module, added in a recent PR last week, fail intermittently in the CI pipeline (GitHub Actions, Ubuntu 22.04, Go 1.22) but pass consistently when run locally on macOS with the same Go version. The failures are non-deterministic — sometimes all tests pass, sometimes a handful fail. This is blocking releases.

## Root Cause Hypothesis
The task ordering tests likely depend on non-deterministic sort stability. Go's sort.Slice is not stable, and the behavior can differ across runs and platforms. If tasks with equal sort keys are expected in a specific order, the tests will pass when the underlying sort happens to preserve insertion order (common locally with smaller datasets or specific memory layouts) and fail when it doesn't. An alternative hypothesis is a race condition if tests run in parallel in CI but sequentially locally, or a map iteration order dependency (Go randomizes map iteration).

## Reproduction Steps
  1. Identify the task ordering tests added in last week's PR
  2. Run the test suite on Ubuntu 22.04 with Go 1.22 (or in the GitHub Actions environment)
  3. Run the tests repeatedly (e.g., `go test -count=100 ./path/to/task/ordering/...`) to reproduce the intermittent failures
  4. Alternatively, run with `-race` flag to check for race conditions

## Environment
CI: GitHub Actions, Ubuntu 22.04, Go 1.22. Local (reporter): macOS, Go 1.22. Tests pass 100% locally, fail intermittently in CI.

## Severity: high

## Impact
Blocking releases for the team. All CI runs are unreliable, requiring reruns and eroding confidence in the test suite.

## Recommended Fix
1. Examine the task ordering tests from last week's PR for sort stability assumptions — if using sort.Slice, switch to sort.SliceStable or add a tiebreaker key to ensure deterministic ordering. 2. Check for map iteration order dependencies in the task ordering logic. 3. Run `go test -race` to rule out data races. 4. Run `go test -count=100` locally to see if the flakiness can be reproduced outside CI. 5. Review whether CI runs tests in parallel (`-parallel` flag or multiple packages concurrently) which might expose shared state issues.

## Proposed Test Case
Add a test that explicitly verifies sort stability by creating multiple tasks with identical sort keys and asserting that a secondary ordering criterion (e.g., creation time or ID) produces a deterministic result. Additionally, run the existing ordering tests with `-count=100` to confirm the fix eliminates flakiness.

## Information Gaps
- Exact error messages and assertion failure output from CI logs
- CI configuration details (parallel test execution, environment variables, test runner flags)
- Whether the task ordering code uses sort.Slice, maps, or concurrent operations internally
- The specific PR that introduced these tests
