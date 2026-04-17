# Triage Summary

**Title:** Flaky ordering tests in task ordering module due to likely non-deterministic map iteration in Go

## Problem
Five tests added to the task ordering module in a PR merged ~4 days ago fail intermittently in CI but pass consistently on the original developer's Mac. The failures are always from the same group of tests, though not all five fail every run. This is blocking releases.

## Root Cause Hypothesis
The task sorting/ordering implementation almost certainly iterates over a Go map to collect or order tasks by priority or insertion order. Go's map iteration order is intentionally randomized by the runtime (per the language spec), so the output order is non-deterministic. The tests assert exact order (e.g., [A, B, C, D, E]), which passes when the runtime happens to iterate in the expected order and fails when it doesn't. This appears deterministic on one developer's machine because the same Go runtime version and memory layout can produce consistent (but not guaranteed) ordering for small maps across repeated runs on the same binary.

## Reproduction Steps
  1. Run the task ordering test suite repeatedly: `go test -count=100 -run TestOrdering ./path/to/task/ordering/...`
  2. On most machines, some subset of the 5 ordering tests will fail within 100 iterations
  3. If failures don't appear, try with `-race` or on a different OS/arch, which can change map iteration patterns

## Environment
Go test runner (standard `go test`), CI environment (OS/arch not confirmed but likely Linux vs developer's macOS). No special parallel or randomization flags reported.

## Severity: high

## Impact
Blocking all releases for the team. Every CI run has a chance of failing, requiring manual re-runs and eroding confidence in the test suite.

## Recommended Fix
1. Find the PR merged ~4 days ago that added the task ordering tests. 2. In the sorting/ordering implementation, look for any iteration over a Go map (e.g., `for k, v := range someMap`). 3. Replace the map with a slice, or collect map keys/values into a slice and sort deterministically before use. 4. If a map is required for lookup, ensure the final ordering step sorts by an explicit, stable key (priority + a tiebreaker like task ID or name) rather than relying on iteration order. 5. Verify the fix by running `go test -count=100` to confirm tests pass consistently.

## Proposed Test Case
Add a test that creates tasks with identical priority values and asserts that the sort output is stable (deterministic tiebreaking by secondary key like task ID). Run with `-count=1000` to verify no flakiness.

## Information Gaps
- Exact PR number and code diff (developer can find this — merged ~4 days ago, touches task ordering module)
- Exact CI configuration (not needed to pursue the fix, but worth reviewing for other potential issues)
- Whether the map usage is in the production sort logic or only in the test setup (both are possible; fix approach is the same)
