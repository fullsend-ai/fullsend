# Triage Summary

**Title:** New task ordering tests fail intermittently in CI due to likely non-deterministic sort

## Problem
Five tests added ~4 days ago in the task ordering module pass consistently on the developer's local machine but fail intermittently in CI. All failures are confined to these new tests; existing tests are unaffected.

## Root Cause Hypothesis
The tests assert a specific element order after sorting, but the sort is non-deterministic for elements with equal sort keys. Go's sort.Slice uses an unstable sort algorithm, so items with equal keys can appear in any order between runs. The local machine likely produces a consistent order by coincidence (same hardware, memory layout, no parallelism), while CI exposes the non-determinism through parallel execution, the race detector, or different system characteristics.

## Reproduction Steps
  1. Identify the 5 new tests in the task ordering module from the merged PR (~4 days ago)
  2. Run them repeatedly with: go test -count=100 -shuffle=on ./path/to/task/ordering/...
  3. If that doesn't reproduce, try with the race detector: go test -race -count=100 -shuffle=on ./path/to/task/ordering/...
  4. Check if CI runs with -race or -parallel flags that differ from local defaults

## Environment
Go 1.22, CI environment (specific CI system unknown). Local development machine passes consistently.

## Severity: high

## Impact
Blocking releases for the team. Flaky CI erodes confidence in the test suite and slows all contributors.

## Recommended Fix
1. Inspect the sort call in the task ordering module — if using sort.Slice, check whether the 'less' function fully disambiguates all elements (i.e., has a tiebreaker on a unique field like ID or created_at). 2. If elements can have equal sort keys, either add a stable tiebreaker to the sort, use sort.SliceStable, or update test assertions to only check ordering of elements with distinct keys (e.g., assert relative order only where sort keys differ). 3. Check CI configuration for -race, -parallel, or -shuffle flags that differ from local defaults.

## Proposed Test Case
Create a test that generates multiple tasks with identical sort keys (e.g., same priority and due date) and verifies that the sort output is deterministic by running it in a loop (count=100+). The test should either confirm the tiebreaker produces a unique order or assert only the properties that the sort guarantees (e.g., group ordering rather than exact position of equal elements).

## Information Gaps
- Exact CI system and configuration (parallel flags, race detector, shuffle mode)
- Whether the sort uses sort.Slice vs sort.SliceStable
- Whether any test tasks share identical sort key values
- Exact test names and assertion style (strict order vs. partial order)
