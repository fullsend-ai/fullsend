# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to non-deterministic result ordering

## Problem
Five new task ordering tests added approximately 4 days ago pass consistently on the developer's local macOS machine but fail intermittently in GitHub Actions CI. When they fail, assertion errors show that tasks are returned in an unexpected order (e.g., expected [A, B, C] but got [B, A, C]). This is blocking releases.

## Root Cause Hypothesis
The task ordering tests assert on a specific element order, but the underlying query or data structure does not guarantee deterministic ordering. Locally on macOS the order happens to be stable (likely due to filesystem, memory layout, or platform-specific behavior differences), while the Ubuntu CI runner exposes the non-determinism. Common causes: missing ORDER BY clause in a database query, reliance on map iteration order in Go, use of an unstable sort, or concurrent goroutines writing results without synchronization.

## Reproduction Steps
  1. Identify the PR merged ~4 days ago that added the task ordering tests
  2. Run `go test ./... -count=10` on the package containing the task ordering tests to surface non-deterministic failures locally
  3. Alternatively, trigger a CI run on the main branch and observe the task ordering test results

## Environment
CI: GitHub Actions, Ubuntu 22.04, Go 1.22. Local: macOS, Go 1.22.

## Severity: high

## Impact
Blocks all releases for the team. Every CI run has a chance of failing, requiring manual re-runs and eroding confidence in the test suite.

## Recommended Fix
Examine the task ordering tests from the recent PR. Check whether the code under test guarantees a deterministic order (e.g., via an explicit ORDER BY or stable sort). If ordering is not semantically required, change the test assertions to be order-independent (e.g., sort both slices before comparing, or use an unordered set comparison). If ordering is required, ensure the implementation enforces it with a tiebreaker on a unique field. Run with `-count=100` to verify the fix eliminates flakiness.

## Proposed Test Case
Run the task ordering tests with `go test -count=100 -race` to confirm they pass deterministically across 100 iterations with race detection enabled.

## Information Gaps
- Exact test names and file paths (developer can find via the recent PR)
- Exact CI log output from a failed run (developer can check GitHub Actions history)
- Whether tests are run in parallel in CI via `-parallel` flag or a matrix strategy
