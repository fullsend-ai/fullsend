# Triage Summary

**Title:** Search index includes archived tasks and misses recently created active tasks after v2.3.1 upgrade

## Problem
After upgrading to TaskFlow v2.3.1, search results include archived tasks that should be filtered out and omit active tasks that exist. The issue is consistent across all searches and has been confirmed by at least two users.

## Root Cause Hypothesis
The v2.3.1 upgrade likely introduced a regression in the search indexing or query filtering logic. Possible causes: (1) a search index rebuild during the upgrade failed to apply the archived-task filter, (2) newly created tasks post-upgrade are not being indexed, or (3) a query-layer change removed or inverted the status filter that excludes archived tasks.

## Reproduction Steps
  1. Log into TaskFlow web app (v2.3.1)
  2. Create a new task with 'Q2 planning' in the title
  3. Ensure at least one archived task also contains 'Q2 planning'
  4. Use the search feature to search for 'Q2 planning'
  5. Observe that the archived task appears in results and the newly created active task does not

## Environment
TaskFlow v2.3.1 (upgraded a few days ago), web app, Chrome (latest), issue confirmed by multiple users

## Severity: high

## Impact
All users performing searches are affected. Search returns misleading results — surfacing irrelevant archived tasks and hiding active ones — undermining core task-finding functionality.

## Recommended Fix
Investigate changes to search indexing and query filtering in the v2.3.1 release. Check whether (1) the search index was properly rebuilt post-upgrade with correct status filters, (2) new tasks created after the upgrade are being added to the index, and (3) the query layer correctly filters on task status. Compare the search query logic between v2.3.0 and v2.3.1. A search index rebuild with correct filtering may resolve the issue.

## Proposed Test Case
Create an archived task and an active task with the same keyword. Perform a search for that keyword. Assert that only the active task appears in results and the archived task is excluded. Additionally, verify that tasks created after an upgrade are indexed and searchable within a reasonable timeframe.

## Information Gaps
- Exact Chrome version (unlikely to be relevant given this is a server-side search issue)
- Whether the search backend uses a separate index (e.g., Elasticsearch) that may need manual reindexing after upgrades
- Whether the issue affects API-level search or only the UI search
