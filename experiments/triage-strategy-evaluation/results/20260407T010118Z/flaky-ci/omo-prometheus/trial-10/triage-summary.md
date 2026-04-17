# Triage Summary

**Title:** Flaky task ordering tests in CI due to likely unstable sort with duplicate sort keys

## Problem
Unit tests in the task ordering module fail intermittently in CI (Ubuntu) but pass consistently on the reporter's macOS machine. The flakiness began ~4 days ago when a colleague merged a PR adding new ordering tests that assert tasks are returned in insertion order after sorting.

## Root Cause Hypothesis
The new tests likely use `sort.Slice` (which is not stable in Go) to sort tasks that have duplicate sort keys (e.g., same priority or timestamp). On macOS, the memory allocator happens to produce layouts where equal elements consistently maintain insertion order, so tests always pass. On Linux (CI), different memory allocation patterns cause equal elements to be reordered differently, producing intermittent failures. This is a well-known Go pitfall — `sort.Slice` makes no guarantees about the relative order of equal elements.

## Reproduction Steps
  1. Check out the branch with the colleague's ordering test PR merged
  2. Run `go test -count=100 -run TestTaskOrdering ./path/to/ordering/package` on a Linux machine or in the CI environment
  3. Observe intermittent failures where tasks with identical sort keys appear in a different order than insertion order
  4. Alternatively, run with `-shuffle=on` flag which may also surface the issue on macOS

## Environment
CI: Ubuntu, Go 1.22, `go test ./...`. Local: macOS, Go 1.22, possibly with `-race` flag via GOFLAGS. No database involved — pure unit tests. No parallel test execution.

## Severity: high

## Impact
Blocking releases for the team. CI pipeline is unreliable (~50% failure rate on re-runs), eroding confidence in the test suite and slowing development velocity.

## Recommended Fix
1. Inspect the colleague's PR for uses of `sort.Slice` in the task ordering module. 2. Check whether test fixtures contain tasks with identical sort keys (priority, timestamp, etc.). 3. Fix by one of: (a) switch to `sort.SliceStable` if insertion-order preservation for equal elements is desired behavior, (b) add a deterministic tiebreaker to the sort comparator (e.g., task ID), or (c) update test assertions to only verify ordering between tasks with distinct sort keys. Option (b) is recommended as it makes the production sort fully deterministic regardless of platform.

## Proposed Test Case
Create a test that explicitly constructs multiple tasks with identical sort keys (same priority and timestamp) and verifies that after sorting, the result is deterministic. Run this test with `-count=1000` on both macOS and Linux to confirm stability. Additionally, verify that tasks with different sort keys are always ordered correctly.

## Information Gaps
- Have not confirmed whether `sort.Slice` vs `sort.SliceStable` is actually used in the code — reporter was asked but hasn't checked yet
- Have not confirmed whether test fixtures contain duplicate sort keys — reporter was asked but hasn't checked yet
- Exact CI pipeline YAML and flags not yet reviewed (reporter unsure if CI uses same GOFLAGS as local dev)
- The specific PR that introduced the new tests has not been identified by number or link
- Exact test names and package path not provided
