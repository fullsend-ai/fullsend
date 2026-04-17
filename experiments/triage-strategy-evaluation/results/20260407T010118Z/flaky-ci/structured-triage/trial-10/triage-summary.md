# Triage Summary

**Title:** Flaky ordering tests in CI: assertion failures due to non-deterministic task ordering in in-memory Go code

## Problem
The ordering tests added last week fail intermittently in GitHub Actions CI with assertion errors — tasks are returned in an unexpected order. The same tests pass consistently on the developer's local Mac. The code is purely in-memory Go with no database involved, and tests run sequentially in both environments.

## Root Cause Hypothesis
The task ordering code likely relies on non-deterministic iteration order, most probably Go map iteration (which is intentionally randomized by the Go runtime). Alternatively, the code may use sort.Sort or slices.Sort on elements with equal sort keys, producing an unstable ordering that varies across runs and platforms. The tests assert an exact order that happens to be stable on the developer's Mac but is not guaranteed.

## Reproduction Steps
  1. Push the branch with the new ordering tests to GitHub
  2. Trigger a GitHub Actions CI run on Ubuntu runners
  3. Observe that the ordering test suite intermittently fails with assertion errors on task order
  4. Run the same tests locally on Mac — they pass consistently

## Environment
CI: GitHub Actions, Ubuntu runners. Local: macOS. Language: Go. All in-memory, no database. Tests run sequentially in both environments.

## Severity: high

## Impact
Blocking releases for the team. Every CI run has a chance of spurious failure, requiring manual re-runs and eroding confidence in the test suite.

## Recommended Fix
Inspect the ordering tests added last week and the code they exercise. Look for (1) iteration over a Go map whose keys feed into the result order, (2) use of an unstable sort (sort.Slice/sort.Sort) on elements with equal comparison keys, or (3) any other source of non-determinism. Fix by either ensuring a deterministic, fully-specified sort (add a tiebreaker such as ID), switching to sort.SliceStable, or updating test assertions to be order-insensitive where exact order is not a product requirement.

## Proposed Test Case
After applying the fix, run the ordering tests 100 times in a loop (`go test -count=100 -run TestOrdering ./...`) on both Mac and an Ubuntu environment to confirm the flakiness is eliminated.

## Information Gaps
- Exact test names and assertion output (available in CI logs, which the developer pointed to but did not paste)
- Whether the ordering code iterates over a map or uses a sort — requires code inspection
