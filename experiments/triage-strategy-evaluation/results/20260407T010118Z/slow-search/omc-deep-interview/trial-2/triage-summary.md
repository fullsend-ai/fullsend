# Triage Summary

**Title:** Search regression in v2.3: description/full-text searches take 10-15s with large task counts

## Problem
Since upgrading to v2.3 approximately two weeks ago, main search bar queries that match task description content take 10-15 seconds to return results. Title-only searches remain fast. The user has ~5,000 tasks, many with very long descriptions (pasted meeting notes). The slowdown is consistent and reproducible, not intermittent.

## Root Cause Hypothesis
v2.3 likely introduced a regression in how description content is searched — possibly a removed or broken full-text index on the descriptions column, a change from indexed full-text search to unindexed LIKE/regex scanning, or a new search code path that performs per-row description scanning instead of using an index. The title search remaining fast suggests title indexing is intact while description indexing is not.

## Reproduction Steps
  1. Set up TaskFlow v2.3 with a user account containing ~5,000 tasks
  2. Ensure many tasks have large description fields (multi-paragraph text)
  3. Use the main search bar to search for a term that appears in task descriptions but not titles
  4. Observe 10-15 second response time
  5. Search for a term that matches a task title — observe fast response for comparison

## Environment
TaskFlow v2.3, work laptop (OS and browser not specified), ~5,000 tasks accumulated over 2 years, many tasks with long-form description content

## Severity: medium

## Impact
Affects users with large task counts who rely on searching description content. Power users and long-term users are most impacted. Workaround exists: searching by task title still returns quickly.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3 — look for changes to how description fields are queried (index removal, query plan changes, ORM changes). Check whether a full-text index on the task descriptions table exists and is being used. Run EXPLAIN/ANALYZE on the search query with description matching to confirm whether it's doing a sequential scan. If an index was dropped or a migration missed it, restore it. If the search code path changed, compare query plans between title-only and description-inclusive searches.

## Proposed Test Case
Performance test: with a dataset of 5,000+ tasks (including tasks with 1KB+ descriptions), assert that a full-text search query matching description content returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 to confirm regression and verify fix.

## Information Gaps
- Exact OS and browser/client being used
- Whether other v2.3 users with large task counts experience the same issue (isolated vs. widespread)
- Database backend type and version (SQLite, PostgreSQL, etc.)
- Whether searching for a term in a short description is also slow (would confirm it's about the search path, not content size)
