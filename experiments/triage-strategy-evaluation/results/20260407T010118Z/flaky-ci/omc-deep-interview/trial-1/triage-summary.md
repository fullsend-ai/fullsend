# Triage Summary

**Title:** Flaky task-ordering tests due to unstable sort with equal sort keys

## Problem
Five task-ordering tests added in a recent PR fail intermittently in CI while passing consistently on the developer's local machine. The tests assert exact element positions after sorting, but some test items share the same sort key (e.g., priority). This makes the expected order depend on sort stability, which is not guaranteed.

## Root Cause Hypothesis
The sort implementation (likely Go's sort.Slice, which is not stable) does not guarantee a deterministic order for elements with equal sort keys. Tests assert exact positions, so when two equal-keyed items swap — which happens nondeterministically across environments and runs — the test fails. Local runs appear stable because the same compiled binary on the same machine tends to produce the same memory layout.

## Reproduction Steps
  1. Identify the 5 task-ordering tests in the task ordering module (from last week's merged PR)
  2. Examine test data for items with equal sort keys (same priority, due date, etc.)
  3. Run the tests in a loop: `go test -count=100 ./path/to/task/ordering/...`
  4. Observe that some runs fail when equal-keyed items appear in a different order

## Environment
CI pipeline (exact runner unknown); locally reproduced with `go test ./...` on developer's machine (always passes). Go project.

## Severity: high

## Impact
Blocking releases for the team for several days. Developers are working around it by re-running the pipeline, wasting CI resources and time.

## Recommended Fix
Two complementary changes: (1) Make the sort stable by adding a tiebreaker field (e.g., task ID or creation timestamp) so that items with equal primary sort keys always resolve to the same order. Use sort.SliceStable or add the tiebreaker to the comparator in sort.Slice. (2) Review the test assertions — if the product requirement is only that higher-priority items precede lower-priority ones, relax assertions to check the ordering contract rather than exact positions. If exact order IS required, the tiebreaker in step 1 is the fix.

## Proposed Test Case
Add a test with multiple tasks sharing identical sort keys and verify that the output order is deterministic across 100+ runs (`go test -count=100`). Assert that the tiebreaker field (e.g., ID) governs the order of equal-keyed items.

## Information Gaps
- Exact sort field(s) used and whether a tiebreaker already exists but isn't applied
- Whether the product requirement demands a fully deterministic order or only relative ordering by priority
- CI runner configuration details (parallelism, Go version) — unlikely to change the fix direction but could explain why local vs CI behavior diverges
