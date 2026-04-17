# Triage Summary

**Title:** Search index status flag inverted after v2.3.1 migration — active/archived filter returns opposite results

## Problem
After the v2.3.1 patch and its associated database migration, the search index returns archived tasks when filtering for active tasks and vice versa. The underlying task data is correct (direct task access shows correct status), but the search index has inverted status values for all tasks.

## Root Cause Hypothesis
The v2.3.1 database migration inverted the boolean/enum status flag in the search index. Most likely the migration script that updated the search index mapped active→archived and archived→active, or a boolean field's semantics were flipped (e.g., `is_archived` changed to `is_active` without inverting existing values). The primary data store is unaffected — only the search index is corrupt.

## Reproduction Steps
  1. Log into the TaskFlow web app
  2. Use the search bar to search for any known term (e.g., 'Q2 planning')
  3. Observe that the default 'active only' filter returns archived tasks
  4. Switch the filter to 'archived only'
  5. Observe that active tasks (including recently created ones) now appear
  6. Confirm that opening any task directly shows the correct status

## Environment
TaskFlow web app, post-v2.3.1 patch (applied ~3 days ago), following a maintenance window with database migration

## Severity: high

## Impact
All users are affected. Search is effectively unusable under default settings since most users have more archived tasks (reporter has ~300) than active ones (~150), flooding results with irrelevant old tasks. Workaround exists (manually invert the filter), but users are unlikely to discover it on their own.

## Recommended Fix
1. Inspect the v2.3.1 migration script for any transformation of the status/is_archived field in the search index. 2. Check whether the boolean semantics were flipped (e.g., `is_archived` → `is_active` without inverting values). 3. Fix the migration or write a corrective migration that re-inverts the flag. 4. Trigger a full search reindex from the primary data store to ensure index consistency. 5. Verify that new tasks created after the migration also have the correct index value.

## Proposed Test Case
After the fix, create a new active task and archive an existing task. Search with 'active only' filter and confirm the new active task appears and the newly archived task does not. Search with 'archived only' filter and confirm the reverse. Validate across a sample of pre-migration tasks to ensure the corrective migration applied correctly.

## Information Gaps
- Exact migration script contents and which search index technology is used (Elasticsearch, database-backed, etc.)
- Whether tasks created after the migration also have inverted index status, or only pre-migration tasks were affected
- Whether any other indexed fields beyond status were affected by the migration
