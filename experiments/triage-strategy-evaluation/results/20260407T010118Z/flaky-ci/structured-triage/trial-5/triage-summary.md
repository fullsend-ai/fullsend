# Triage Summary

**Title:** Flaky CI: task ordering tests fail intermittently due to likely non-deterministic sort

## Problem
Five tests added in a recent task ordering PR (merged ~4 days ago) fail intermittently in CI but pass consistently on the developer's local machine. The tests assert on the order tasks are returned after sorting. Failures block releases.

## Root Cause Hypothesis
Non-deterministic sort order. In Go, the standard sort (sort.Slice, sort.Sort) is not stable — elements comparing as equal can appear in any order. If the task ordering tests assert on exact output order for items with equal sort keys, results will vary across runs. Go's map iteration order is also randomized, which could contribute if tasks are collected from a map before sorting. The behavioral difference between local (macOS) and CI (Ubuntu) is consistent with platform-dependent memory layout or scheduling differences affecting an unstable sort.

## Reproduction Steps
  1. Identify the 5 task ordering tests added in the PR merged approximately 4 days ago
  2. Run those tests repeatedly (e.g., `go test -count=100 ./path/to/task/ordering/...`) to reproduce the intermittent failure
  3. Inspect the test assertions: check whether they assert on exact slice order for items that may have equal sort keys

## Environment
CI: GitHub Actions on Ubuntu, Go 1.22. Local (reporter): macOS, Go 1.22.

## Severity: high

## Impact
Blocks releases for the team. Developers are wasting time re-running the CI pipeline hoping for green builds.

## Recommended Fix
1. Find the 5 new tests in the task ordering module from the recent PR. 2. Check whether the sort used is stable (`sort.SliceStable`) or unstable (`sort.Slice`). If unstable, either switch to a stable sort or add a tiebreaker (e.g., sort by ID as a secondary key). 3. Update test assertions to either use a stable sort, assert only on the relative order of items with distinct sort keys, or normalize equal-key items before comparison. 4. Run `go test -count=100` to confirm flakiness is resolved.

## Proposed Test Case
Create a test with multiple tasks sharing the same sort key (e.g., same priority and due date) and verify that the sort output is deterministic across 100+ runs, asserting either on stable sort order or on order-independent equality for equal-key groups.

## Information Gaps
- Exact test names and file paths (available in CI logs — reporter declined to provide)
- Exact CI failure output and error messages
- Whether tests are run in parallel in CI (reporter unsure)
- Exact PR reference for the task ordering change
