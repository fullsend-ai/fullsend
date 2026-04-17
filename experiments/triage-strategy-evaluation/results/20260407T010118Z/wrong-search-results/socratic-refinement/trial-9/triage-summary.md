# Triage Summary

**Title:** Search filter for active/archived status is inverted since v2.3.1 database migration

## Problem
After the v2.3.1 update (which included a database migration ~3 days ago), the search feature returns archived tasks when filtering for active tasks, and active tasks when filtering for archived tasks. The task data itself is correct — browsing and direct task views show the right status. The issue is isolated to search filtering.

## Root Cause Hypothesis
The v2.3.1 database migration likely inverted the boolean or enum value representing archived/active status in the search index. For example, if the migration changed the schema from an 'active' flag to an 'archived' flag (or vice versa) but the search query logic was not updated to match, or if the migration re-indexed tasks with the flag value flipped, the search filter would return exactly the opposite of what's requested — which matches the reported behavior perfectly.

## Reproduction Steps
  1. Log in as any user with both active and archived tasks
  2. Use the search feature with the default 'active tasks only' filter
  3. Search for a term that matches both an active and an archived task (e.g., 'Q2 planning')
  4. Observe that archived tasks appear in results and the active task is missing
  5. Switch the search filter to 'archived tasks' and observe that active tasks now appear

## Environment
v2.3.1 (post-database migration), affects multiple users, user has ~150 active and ~300 archived tasks

## Severity: high

## Impact
All users are affected. Search is effectively unusable for finding tasks since results are inverted. Users must manually browse task lists instead of searching, which does not scale.

## Recommended Fix
Examine the v2.3.1 database migration for changes to the archived/active status field. Check whether the search index (or search query logic) interprets the status value with the correct polarity. Likely fix is either: (a) correct the search query to match the new schema, or (b) run a re-indexing operation to align the search index with the migrated data. Also verify that any status-based filtering in the search layer matches the post-migration column semantics.

## Proposed Test Case
Create a user with known active and archived tasks. Search with the 'active only' filter and assert that only active tasks are returned. Search with the 'archived' filter and assert that only archived tasks are returned. Run this test against both pre-migration and post-migration schema to catch polarity inversions.

## Information Gaps
- Which specific search technology is used (database query, Elasticsearch, etc.) — a developer would know this from the codebase
- Exact migration SQL/script content — a developer can inspect the v2.3.1 migration files directly
