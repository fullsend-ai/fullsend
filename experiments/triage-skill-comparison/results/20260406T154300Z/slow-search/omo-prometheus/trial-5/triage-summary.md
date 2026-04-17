# Triage Summary

**Title:** Search on task descriptions regressed to 10-15s in v2.3 (title search unaffected)

## Problem
Since updating to v2.3, searching through task descriptions takes 10-15 seconds per query. Title-only search remains fast. The user has ~5,000 tasks accumulated over two years, many with long descriptions containing pasted meeting notes.

## Root Cause Hypothesis
v2.3 likely introduced a regression in description search — most probably a dropped or missing full-text index on the descriptions column, a switch from indexed search to naive full-text scan, or a new query path that doesn't use the existing index. The fact that title search is unaffected points to a change specific to description search logic rather than a general database or infrastructure issue.

## Reproduction Steps
  1. Set up a TaskFlow instance running v2.3
  2. Populate with ~5,000 tasks, ensuring many have long descriptions (multi-paragraph text, e.g., pasted meeting notes)
  3. Perform a search that targets task descriptions (not title-only)
  4. Observe query time — expect 10-15 seconds
  5. Compare with the same search on v2.2 to confirm regression

## Environment
TaskFlow v2.3, work laptop (OS/specs unknown), ~5,000 tasks with long descriptions

## Severity: high

## Impact
Affects any user with a substantial task history who searches by description content. Search is a core workflow feature — 10-15 second latency on every query significantly degrades usability. Users with large datasets and long descriptions are most affected.

## Recommended Fix
1. Diff the search query/logic between v2.2 and v2.3 to identify what changed for description search. 2. Check whether a full-text index on the descriptions column exists and is being used (run EXPLAIN/query plan analysis). 3. If the index was dropped or a migration missed it, restore it. 4. If the search strategy changed (e.g., from database-level full-text search to application-level string matching), revert or optimize. 5. Consider adding pagination or result-count limits if unbounded result sets contribute to latency.

## Proposed Test Case
Performance test: with a dataset of 5,000+ tasks (descriptions averaging 500+ words), assert that description search returns results in under 2 seconds. Run this test against both the fix and the v2.2 baseline to confirm parity.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the title-vs-description split)
- Whether other v2.3 users with large datasets report the same issue
- Exact v2.3 changelog entries related to search
- Database engine in use (SQLite vs PostgreSQL vs other) — index behavior varies
