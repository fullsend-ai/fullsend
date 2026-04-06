# Triage Summary

**Title:** Search on task descriptions is extremely slow since v2.3 upgrade (~12s for 5,000 tasks)

## Problem
Full-text search against task description fields takes 10-15 seconds to return results, while searches matching task titles return almost instantly. The reporter has approximately 5,000 tasks and the slowness began after upgrading from TaskFlow 2.2 to 2.3.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in how description text is searched — possibly a missing or dropped database index on the task descriptions column, a change from indexed full-text search to unindexed LIKE/ILIKE queries, or a search implementation change that scans descriptions without leveraging an index. Title searches remain fast because the title index was unaffected.

## Reproduction Steps
  1. Have an account with a large number of tasks (~5,000)
  2. Open the main search bar
  3. Search for a keyword that appears in task descriptions but not titles (e.g., 'meeting notes', 'budget review')
  4. Observe that results take 10-15 seconds to return
  5. Search for a keyword that matches a task title and observe near-instant results

## Environment
Ubuntu 22.04, ThinkPad T14, TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago), ~5,000 tasks

## Severity: high

## Impact
Any user with a non-trivial number of tasks experiences severely degraded search performance when searching description text, which is a core workflow. The regression affects all description-based searches regardless of query term.

## Recommended Fix
Compare the v2.2 and v2.3 database migrations and search query logic. Check whether a full-text index on the task descriptions column was dropped, altered, or is no longer being used by the search query path. Verify the query plan (EXPLAIN ANALYZE) for description searches against a dataset of ~5,000 tasks. If an index is missing, add it back; if the query strategy changed, restore indexed full-text search for descriptions.

## Proposed Test Case
Create a test with 5,000+ tasks with varied description text. Execute a search for a term that matches only in descriptions and assert that results return within an acceptable threshold (e.g., under 1 second). Also verify that the search query plan uses an index scan rather than a sequential scan on the descriptions column.

## Information Gaps
- No server-side logs or query timing data to confirm the bottleneck is database-side vs. application-side
- Unknown whether other users with large task counts on v2.3 are also affected
- Browser developer tools network timing not provided (would confirm if latency is server response time vs. client rendering)
