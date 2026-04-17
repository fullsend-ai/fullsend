# Triage Summary

**Title:** Flaky ordering tests from recent PR: unstable sort produces non-deterministic results in CI

## Problem
Ordering tests introduced in a recent PR fail intermittently in CI (Ubuntu) with assertion errors — the task ordering comes back wrong. Tests pass consistently on the developer's local machine (macOS). This has been blocking releases for 4 days.

## Root Cause Hypothesis
The task ordering code most likely uses Go's `sort.Slice` or `slices.Sort`, which are unstable sorts. When two tasks have equal sort keys (same priority, timestamp, etc.), their relative order is undefined and varies between runs. The developer's local environment has a dev config that sets GOFLAGS including a fixed shuffle seed (e.g., `-shuffle=12345`), which forces a deterministic test execution order that happens to mask the instability. CI runs without this config, exposing the non-determinism.

## Reproduction Steps
  1. Identify the ordering tests from the recently merged PR
  2. Run them without the local dev config: `go test -count=10 -shuffle=on ./path/to/ordering/tests/...`
  3. Observe intermittent assertion failures where tasks with equal sort keys appear in different orders across runs
  4. Compare with running under the dev config's GOFLAGS to confirm the config masks the issue

## Environment
CI: Ubuntu, Go 1.22, no special GOFLAGS. Local: macOS, Go 1.22, dev config sets GOFLAGS with -race and a seed value (exact contents not confirmed).

## Severity: high

## Impact
Blocking all releases for 4+ days. The entire team is affected. No workaround other than re-running CI until tests happen to pass.

## Recommended Fix
1. Switch the sort from `sort.Slice`/`slices.Sort` to `sort.SliceStable`/`slices.SortStableFunc`, OR add a tiebreaker field (e.g., task ID) to the sort comparator so no two elements are ever equal. 2. Fix the tests to either assert stable ordering only on the distinguishing fields, or sort the expected and actual results by a unique key before comparing. 3. Audit the dev config — if it sets a fixed `-shuffle` seed, either remove it or replicate it in CI so local and CI behavior match. 4. Consider adding `-shuffle=on` to CI to proactively catch ordering assumptions in future tests.

## Proposed Test Case
Create a test with multiple tasks sharing identical sort keys (e.g., same priority and timestamp) and verify that the sort output is deterministic by running with `-count=100`. The test should either assert on a stable sort or assert only on the primary ordering invariant (e.g., 'all priority-1 tasks appear before priority-2') without requiring a specific order among equal-priority tasks.

## Information Gaps
- Exact contents of the dev config GOFLAGS (specifically the seed value and other flags)
- Exact test names and file paths for the failing ordering tests
- Which sort function is used in the implementation (sort.Slice vs slices.Sort vs other)
- Which fields the sort comparator uses and whether any test cases have equal values for those fields
- Exact CI configuration (how go test is invoked)
