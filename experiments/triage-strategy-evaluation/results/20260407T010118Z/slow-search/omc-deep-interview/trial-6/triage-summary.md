# Triage Summary

**Title:** v2.3 regression: full-text description search is CPU-bound and takes 10-15s (~5,000 tasks)

## Problem
After updating to v2.3, searching task descriptions via the main search bar takes 10-15 seconds consistently, regardless of query specificity or number of matches. Title search remains fast. The search is CPU-intensive (laptop fan spins up), indicating a computational bottleneck rather than a network or I/O wait. The user has ~5,000 tasks, some with long descriptions (pasted meeting notes), and reports this previously worked fine.

## Root Cause Hypothesis
v2.3 likely changed the description search implementation — most probably removed or broke a search index, switched from indexed/optimized search to a naive full-text scan across all task descriptions, or introduced an inefficient regex/string matching approach. The fact that title search is still fast suggests titles may still use an index while descriptions now do a brute-force scan. The CPU spike confirms the work is computational (scanning/matching text) rather than waiting on a database query or network call.

## Reproduction Steps
  1. Set up TaskFlow v2.3
  2. Create or import ~5,000 tasks, including some with long descriptions (e.g., multi-paragraph meeting notes)
  3. Open the main search bar
  4. Search for any term that appears in task descriptions (not just titles)
  5. Observe search taking 10-15 seconds with high CPU usage
  6. Compare: search for a term that only appears in task titles — should return quickly

## Environment
TaskFlow v2.3, work laptop (OS unspecified), ~5,000 tasks with some containing lengthy descriptions

## Severity: high

## Impact
Any user with a non-trivial number of tasks is likely affected when searching by description content. The feature is still functional but degraded to the point of being painful (10-15s waits). Title-only search serves as a partial workaround but limits the utility of search significantly.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on how description content is searched. Likely areas: (1) check if a full-text search index on task descriptions was dropped, disabled, or is no longer being updated; (2) check if the search query path changed from an indexed database query to an in-memory or brute-force scan; (3) profile the description search path with ~5,000 tasks to confirm where CPU time is spent. If an index was removed, restore it. If the search algorithm changed, revert or optimize it.

## Proposed Test Case
Performance test: with 5,000 tasks (including ~50 with descriptions over 2,000 characters), a description search query should return results in under 2 seconds. Regression test: compare description search latency between v2.2 and v2.3 with identical datasets to verify the fix restores prior performance.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to affect fix direction since this is an algorithmic regression)
- Whether the issue reproduces on v2.3 with fewer tasks (e.g., 100) — would clarify if it's O(n) scaling or a fixed overhead
- v2.3 changelog details on what changed in the search subsystem
