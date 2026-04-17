# Triage Summary

**Title:** Task ordering tests fail intermittently in CI due to likely non-deterministic iteration order in Go

## Problem
Recently added task ordering tests pass consistently on the developer's Mac but fail intermittently in CI (Ubuntu). Failures are assertion errors where tasks come back in the wrong order. The tests expect tasks to be returned in insertion order after being stored and retrieved in-memory.

## Root Cause Hypothesis
The in-memory task storage most likely uses a Go map, which has intentionally randomized iteration order. Tasks inserted in a specific order are not guaranteed to be retrieved in that order. Go randomizes map iteration as a language design choice (since Go 1.0) specifically to prevent developers from depending on it. On the reporter's Mac, memory layout may coincidentally produce the expected order most of the time, while CI on Ubuntu surfaces the randomness more frequently. An alternative hypothesis is an unstable sort (sort.Slice) with duplicate keys, but the reporter's description of expecting 'insertion order' without explicit sorting points more strongly to map iteration.

## Reproduction Steps
  1. Identify the task ordering test file in the PR merged last week (approximately 5 new test cases in the task ordering module)
  2. Examine the underlying data structure used to store tasks — check if it is a map[K]Task or similar
  3. Run the failing tests with '-count=100' to reproduce locally: 'go test -run TestTaskOrder -count=100 ./...'
  4. If using a map, iteration order will vary across runs, causing intermittent assertion failures

## Environment
CI: Ubuntu (Linux). Local: macOS. Same Go version on both. All in-memory, no database involved.

## Severity: high

## Impact
Blocking releases for the team. The flaky tests erode CI trust and prevent merging other work. The underlying issue (if non-deterministic ordering) could also be a user-facing bug if the retrieval path is shared with production code.

## Recommended Fix
1. Check the storage data structure: if tasks are stored in a map, switch to a slice or use an ordered data structure (slice + map for O(1) lookup if needed). 2. If a sort is involved, ensure it uses sort.SliceStable or includes a tiebreaker field (e.g., insertion timestamp or ID). 3. If insertion order is the intended contract, document and enforce it in the data structure, not just in tests.

## Proposed Test Case
Run the existing ordering tests with '-count=1000' and '-race' to verify the fix eliminates flakiness. Additionally, add a test that inserts tasks with identical sort keys (if applicable) and asserts stable ordering, ensuring the determinism guarantee is explicit.

## Information Gaps
- Exact data structure used for task storage (map vs slice vs other) — requires reading the source code
- Whether the retrieval path used in tests is the same as the production code path
- Specific PR and test file names (reporter can provide the PR link if needed, but test names are visible in CI logs)
