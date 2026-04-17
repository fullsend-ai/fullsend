# Triage Summary

**Title:** Description search regression in v2.3: CPU-bound full scan causes 10-15s latency at ~5k tasks

## Problem
After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds and pegs a CPU core at 100%. Title search remains fast. The slowdown is consistent regardless of query complexity (single common word vs. specific phrase) and occurs on a dataset of ~5,000 tasks, many with long descriptions (pasted meeting notes).

## Root Cause Hypothesis
v2.3 likely introduced a regression in the description search path — most probably removed or broke a full-text index on the description field, or switched from an indexed/optimized search to a brute-force in-memory scan. The CPU saturation (single core at 100%) and query-independent latency are consistent with a linear scan over all description text rather than an index lookup. The fact that title search is unaffected suggests the title field still has a working index while the description field does not.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Create or import a workspace with ~5,000 tasks, including tasks with long descriptions (multi-paragraph text)
  3. Perform a search scoped to task descriptions using any query (e.g., 'meeting')
  4. Observe 10-15 second delay and 100% CPU usage on one core
  5. Compare: perform the same search scoped to task titles only — should return near-instantly
  6. Optionally compare: downgrade to v2.2 and repeat the description search to confirm it was fast in the prior version

## Environment
TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks with some long descriptions, running on a work laptop (OS/specs not specified but not likely relevant given the regression nature)

## Severity: medium

## Impact
Users with large task collections who rely on description search are significantly impacted — the feature is functionally unusable at 10-15s per query. Title search still works as a partial workaround. This is a regression from v2.2, so all users who upgraded to v2.3 with substantial task counts are likely affected.

## Recommended Fix
1. Diff the search implementation between v2.2 and v2.3 to identify what changed in the description search path. 2. Check whether a full-text index on the description field was dropped, altered, or is no longer being used by the query planner. 3. If an index was removed, restore it. If the search algorithm changed, profile the new path and optimize or revert. 4. Consider adding query performance benchmarks against a dataset of ≥5k tasks with realistic description lengths to catch similar regressions in CI.

## Proposed Test Case
Performance test: with a dataset of 5,000 tasks (descriptions averaging 500+ words), assert that a description search for any single-word query returns results in under 2 seconds. Run this against both v2.2 (baseline) and the fix branch to confirm the regression is resolved.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to matter given this is a regression)
- Whether other v2.3 users with large datasets are also affected (likely, but unconfirmed)
- The specific v2.3 changelog entry or commit that changed the search path
