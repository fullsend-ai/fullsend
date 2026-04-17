# Triage Summary

**Title:** Task-ordering unit tests added in recent PR fail intermittently in CI (Ubuntu) but pass locally (macOS)

## Problem
Five unit tests for task ordering logic, merged last week, fail intermittently in the CI pipeline (Ubuntu) while consistently passing on the developer's local machine (macOS). Both environments use Go 1.22. The failures are blocking releases.

## Root Cause Hypothesis
The task-ordering tests almost certainly rely on non-deterministic ordering that happens to resolve consistently on macOS but not on Ubuntu. The most likely candidates in Go unit tests for 'task ordering logic' are: (1) **Map iteration order** — if tests range over a map and expect a specific order, Go randomizes map iteration and results will vary across runs. (2) **Unstable sort** — if tests use `sort.Slice`/`sort.Sort` with a comparator that treats some elements as equal, the relative order of equal elements is not guaranteed and may differ across platforms/runs. (3) **Locale or filesystem ordering differences** — if ordering depends on string comparison or file enumeration that differs between macOS (APFS, case-insensitive) and Ubuntu (ext4, case-sensitive).

## Reproduction Steps
  1. Identify the 5 failing test names from recent CI logs
  2. Run `go test -count=100 -run 'TestName1|TestName2|...' ./path/to/package` locally to attempt reproduction through repeated runs
  3. If they still pass locally on macOS, try running in a Linux container or on Ubuntu to match CI environment
  4. Inspect the test code for map iteration, sort stability assumptions, or any ordering that isn't explicitly enforced

## Environment
CI: Ubuntu (specific version not provided), Local: macOS. Both running Go 1.22. Tests run via `go test` (sequential, no parallelism).

## Severity: high

## Impact
Blocking all releases for the team. Every CI run has a chance of failing, requiring reruns and eroding confidence in the test suite.

## Recommended Fix
Examine the 5 failing task-ordering tests from the recently merged PR. Look for: (1) map iteration used to build expected output — replace with sorted keys or explicit ordering, (2) sort comparators where equal elements exist — use `slices.SortStableFunc` or add a tiebreaker, (3) any assumption that elements arrive in insertion order. The fix is to make ordering assertions order-independent (e.g., sort both actual and expected before comparing) or to enforce deterministic ordering in the code under test.

## Proposed Test Case
After fixing, run `go test -count=500 -run 'FailingTestNames' ./path/to/package` on both macOS and Ubuntu (or in CI) to confirm the tests pass consistently across many iterations. Zero failures across 500 runs on both platforms would confirm the fix.

## Information Gaps
- Exact test names and file paths (available in CI logs, which the developer can access)
- Specific assertion failure messages (would confirm whether it's an ordering mismatch)
- Whether the code under test uses maps or sort internally (developer can verify when examining the code)
