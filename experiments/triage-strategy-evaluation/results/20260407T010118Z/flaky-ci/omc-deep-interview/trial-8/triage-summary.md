# Triage Summary

**Title:** Ordering tests fail intermittently in CI due to non-deterministic map iteration (fixed hash seed locally, randomized in CI)

## Problem
Tests added in a recent PR that verify task ordering fail with assertion errors intermittently in CI but pass consistently on developer machines. The expected order of results does not match the actual order returned.

## Root Cause Hypothesis
The ordering tests rely on the natural iteration order of a Go map or similar unordered data structure. The local dev environment has a fixed hash seed (confirmed via GOFLAGS/env config), which makes map iteration order deterministic and repeatable locally. CI does not fix the hash seed, so Go's intentional map iteration randomization causes the order to vary between runs. The tests likely either (a) assert on results without explicitly sorting them first, or (b) sort by a non-unique key without a stable tiebreaker, exposing the underlying nondeterminism.

## Reproduction Steps
  1. Identify the ordering tests from the recently merged PR (likely in a package related to task ordering/sorting)
  2. Run the tests with a randomized hash seed: `GOFLAGS='' go test -count=100 -shuffle=on ./path/to/ordering/tests/...`
  3. Observe intermittent assertion failures where actual result order differs from expected
  4. Compare to running with the dev environment's fixed hash seed, where tests pass consistently

## Environment
Go (same version locally and CI). Local dev setup includes GOFLAGS with -race and a fixed hash seed. CI environment does not fix the hash seed. CI may also run tests in parallel or with -shuffle, further exposing the nondeterminism.

## Severity: high

## Impact
Blocking releases. The team is working around failures by re-running the CI pipeline until tests happen to pass, which wastes CI resources and delays deployments. All team members are affected.

## Recommended Fix
1. Locate the ordering tests from the recent PR and examine what data structure holds results before assertion. 2. If results come from map iteration or any unordered source, add an explicit sort (e.g., `sort.Slice()`) on deterministic keys before asserting order. 3. If sorting by a field that can have duplicates, add a tiebreaker (e.g., sort by priority then by ID). 4. Remove or stop relying on the fixed hash seed in local dev config — it masks real nondeterminism bugs. 5. Consider adding `-shuffle=on` to the local GOFLAGS so developers catch these issues before CI does.

## Proposed Test Case
Run the ordering tests with `-count=50 -shuffle=on` and no fixed hash seed. All 50 runs should pass consistently. Additionally, add a test case that creates tasks with identical sort keys to verify the tiebreaker produces stable ordering.

## Information Gaps
- Exact test file and function names from the merged PR (team can identify from recent PR history)
- Exact CI pipeline config and test invocation command (team should verify CI does not fix the hash seed)
- The specific fixed hash seed env var name in the dev setup (reporter mentioned it but wasn't sure of details)
- Whether CI also uses -shuffle=on or -parallel flags that compound the issue
