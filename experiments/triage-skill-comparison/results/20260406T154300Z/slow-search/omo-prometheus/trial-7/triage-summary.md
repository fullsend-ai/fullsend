# Triage Summary

**Title:** Search across task descriptions regressed to ~10-15s in v2.3 (title search unaffected)

## Problem
After upgrading from v2.2 to v2.3, searching via the main search bar takes 10-15 seconds consistently when results match on task descriptions. Title-only matches remain fast (<1s). The slowdown is consistent regardless of query complexity, term frequency, or whether it is the first search in a session. The user has ~5,000 tasks accumulated over 2 years, many with long descriptions (pasted meeting notes).

## Root Cause Hypothesis
v2.3 likely dropped, broke, or failed to migrate the full-text search index on the task descriptions column. Alternatively, v2.3 may have changed the description search path from an indexed query to a full-table scan or application-level string matching. Title search still hits a working index, which explains why it remains fast while description search degrades linearly with data volume.

## Reproduction Steps
  1. Have a TaskFlow instance upgraded from v2.2 to v2.3 with a substantial number of tasks (~5,000) containing non-trivial descriptions
  2. Open the main search bar
  3. Search for any term that appears in task descriptions (not just titles)
  4. Observe 10-15 second delay before results appear
  5. Compare: search for a term that only appears in task titles — this should return quickly

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS/specs not specified but irrelevant given the regression is version-correlated), ~5,000 tasks with long descriptions

## Severity: high

## Impact
Any user with a non-trivial task count who searches by description content is affected. Search is a core workflow feature and 10-15s latency makes it effectively unusable. The regression affects all v2.3 users proportionally to their task/description volume.

## Recommended Fix
1. Diff the database schema migrations between v2.2 and v2.3 — look for changes to full-text indexes on the tasks/descriptions table. 2. Check the search query execution plan in v2.3 for description search vs title search. 3. If the index was dropped or altered, restore it. If the query path changed (e.g., switched to LIKE '%term%' or application-level filtering), revert to the indexed approach. 4. Verify with a dataset of ~5,000+ tasks with long descriptions.

## Proposed Test Case
Create a test database with 5,000+ tasks, each with description text of 500+ words. Benchmark main search bar queries that match on description content. Assert that search returns results in under 2 seconds. Run this test against both the v2.2 and v2.3 search code paths to confirm the regression and validate the fix.

## Information Gaps
- Exact database backend (SQLite, PostgreSQL, etc.) — affects index investigation approach
- Whether other v2.3 users with large task counts report the same issue
- v2.3 changelog/migration scripts (not yet examined by a developer)
