# Triage Summary

**Title:** Search index includes archived tasks and omits recently created active tasks since v2.3.1 upgrade

## Problem
After upgrading to TaskFlow v2.3.1, search results include archived tasks that should be filtered out and fail to surface recently created active tasks. The issue affects all searches for at least two users on the same instance.

## Root Cause Hypothesis
The v2.3.1 upgrade likely introduced a search index regression — either the index was rebuilt without respecting the archived/active status filter, or a schema migration changed how task status is indexed. A reindex operation after the upgrade may have included archived tasks or failed to index new active tasks correctly.

## Reproduction Steps
  1. Ensure TaskFlow is on v2.3.1
  2. Create a new active task with a distinctive name (e.g., 'Q2 planning')
  3. Ensure an archived task with the same or similar name exists
  4. Use the search feature to search for that name
  5. Observe that the archived task appears in results and the newly created active task does not

## Environment
TaskFlow v2.3.1 (upgraded a few days ago), web app, Chrome (latest), multiple users affected

## Severity: high

## Impact
All users on the instance are affected — search is a core workflow feature and currently returns misleading results, making it unreliable for finding active work.

## Recommended Fix
Investigate the v2.3.1 changelog for changes to search indexing, task status filtering, or index migration. Compare the search query logic between v2.3.0 and v2.3.1 for how archived status is handled. Check whether a post-upgrade reindex is needed and whether newly created tasks are being indexed at all. If the search index is stale or corrupt, triggering a full reindex with correct status filtering may resolve the issue immediately.

## Proposed Test Case
Create an archived task and an active task with the same keyword. Perform a search for that keyword and assert that only the active task appears in results. Additionally, create a new active task and verify it appears in search results within the expected indexing delay.

## Information Gaps
- Exact Chrome version (unlikely to matter given this appears server-side)
- Whether the issue also affects other search terms beyond 'Q2 planning' with concrete examples (reporter stated all searches are affected)
- Server-side logs or search index state that could confirm the reindex hypothesis
