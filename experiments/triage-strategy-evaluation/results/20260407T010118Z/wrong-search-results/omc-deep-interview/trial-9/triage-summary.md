# Triage Summary

**Title:** Search index status flag inverted after v2.3.1 database migration — active/archived filter returns opposite results

## Problem
After the v2.3.1 update and associated database migration, the search index returns archived tasks when the 'active tasks only' filter is selected, and active tasks when the 'archived tasks' filter is selected. The underlying task data is correct — tasks viewed directly show the right status. The issue affects all searches for all users in the workspace.

## Root Cause Hypothesis
The v2.3.1 database migration likely inverted the status flag (or equivalent boolean/enum) in the search index. The source-of-truth task records were not affected, but the denormalized status value written to the search index during migration has active and archived swapped. This could be a literal boolean inversion (e.g., `is_archived` written as `!is_archived`), a mapping error in an enum conversion, or the migration re-indexed with a filter predicate that was accidentally negated.

## Reproduction Steps
  1. Log in to a workspace that went through the v2.3.1 database migration
  2. Create or identify a task with 'active' status and a known keyword (e.g., 'Q2 planning')
  3. Search for that keyword using the default 'active tasks only' filter
  4. Observe that archived tasks matching the keyword appear, but the active task does not
  5. Switch the search filter to 'archived tasks'
  6. Observe that the active task now appears in results

## Environment
TaskFlow v2.3.1, post-database-migration. Workspace with ~300 archived and ~150 active tasks. Confirmed on at least two user accounts.

## Severity: high

## Impact
All users in affected workspaces cannot use search effectively — every search returns inverted results. Workaround exists (manually select the opposite filter), but this is confusing and unreliable for users who don't realize the inversion. Core search functionality is broken.

## Recommended Fix
1. Inspect the v2.3.1 migration script for how task status was written to the search index — look for a boolean inversion or negated predicate. 2. Check the search index entries directly (e.g., query Elasticsearch/equivalent) to confirm the status field is inverted relative to the source database. 3. Fix the migration logic and re-index all tasks from the source-of-truth data. 4. Verify that the search service's filter query correctly maps 'active' filter to the non-archived status value (in case the inversion is in the query layer rather than the index).

## Proposed Test Case
After re-indexing: search for a known active task with the 'active tasks only' filter and confirm it appears; search with the 'archived' filter and confirm it does not. Repeat for a known archived task (should appear under archived filter, not active). Run across a dataset with mixed statuses to confirm no tasks leak across filters.

## Information Gaps
- Which search backend is used (Elasticsearch, database full-text, etc.) — determines exact fix approach
- Whether the issue affects all workspaces or only those that went through migration during the specific maintenance window
- Exact migration script that ran during v2.3.1 — not yet inspected
