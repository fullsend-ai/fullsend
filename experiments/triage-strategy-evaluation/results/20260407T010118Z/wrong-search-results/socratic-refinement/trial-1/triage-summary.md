# Triage Summary

**Title:** Search archive filter inverted after v2.3.1 migration — active filter returns archived tasks and vice versa

## Problem
Since the v2.3.1 update (approximately three days ago), the search feature's archive filter is inverted. Searching with the default 'active tasks only' filter returns archived tasks, while switching to the 'archived' filter shows active tasks. The underlying task data is correct — individual task pages display the correct archive status. The issue is isolated to how search interprets or applies the archive flag.

## Root Cause Hypothesis
The database migration included in v2.3.1 likely inverted the boolean value or enum representing archive status in the search index (e.g., flipped a 0/1 flag, negated an 'is_archived' column, or rebuilt the search index with inverted filter logic). Since the task records themselves are correct but search filtering is backwards, the problem is almost certainly in the search index or the query that filters on archive status — not in the task data itself.

## Reproduction Steps
  1. Create a new task with a distinctive name (e.g., 'triage test active') and leave it active
  2. Archive a different task with a distinctive name (e.g., 'triage test archived')
  3. Search for 'triage test' with the default 'active tasks only' filter
  4. Observe that the archived task appears and the active one does not
  5. Switch the filter to 'archived tasks' and observe that the active task now appears

## Environment
TaskFlow v2.3.1 (upgraded approximately 3 days ago, including a database migration). Affects multiple users in the same instance.

## Severity: high

## Impact
All users on the instance are affected. Every search with archive filtering returns inverted results, making search effectively unusable for finding active work. Users must manually reverse the filter as a workaround, which is confusing and error-prone.

## Recommended Fix
Examine the v2.3.1 database migration script for changes to the archive status field in the search index or search-related tables. Check whether a boolean was flipped, a column default changed, or a reindexing step inverted the flag. The fix is likely either: (a) a corrective migration that re-inverts the flag in the search index, or (b) a fix to the search query logic if the migration changed the semantics of the field (e.g., from 'is_active' to 'is_archived') without updating the query. Since the source-of-truth task data is correct, a search reindex against the correct task archive status should also resolve it.

## Proposed Test Case
Create one active and one archived task. Assert that searching with the 'active only' filter returns only the active task, and searching with the 'archived' filter returns only the archived task. Run this test against both pre- and post-migration states to catch regressions.

## Information Gaps
- Exact search technology in use (database full-text search, Elasticsearch, etc.) — discoverable from codebase
- Exact contents of the v2.3.1 migration script — discoverable from version control
- Whether any other filter types (e.g., by project, assignee) are also affected — low likelihood given the symptom pattern
