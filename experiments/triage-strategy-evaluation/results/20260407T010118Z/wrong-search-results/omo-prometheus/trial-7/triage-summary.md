# Triage Summary

**Title:** Search status filter inverted after v2.3.1 migration — 'Active only' returns archived tasks and vice versa

## Problem
After the v2.3.1 update and its associated database migration, the search status filter operates in reverse. Selecting 'Active only' returns archived tasks (~300 in reporter's case), while selecting 'Archived' or 'Include archived' surfaces active tasks. The underlying task data is correct — browsing task lists and project boards shows the right statuses. The defect is isolated to the search index.

## Root Cause Hypothesis
The v2.3.1 database migration inverted the boolean or enum value representing active/archived status when rebuilding or updating the search index. Most likely scenario: a schema change flipped the semantics of the flag (e.g., 'is_active' was replaced with 'is_archived' or a boolean was negated) but the search indexing or query logic was not updated to match, causing the filter predicate to select the opposite set of documents.

## Reproduction Steps
  1. Log in to any account that has both active and archived tasks
  2. Navigate to the search interface
  3. Search for any term (e.g., 'Q2 planning') with the default 'Active only' filter
  4. Observe that archived tasks appear in results while known active tasks are missing
  5. Switch the filter to 'Archived' or 'Include archived'
  6. Observe that active tasks now appear in results — the filter is inverted

## Environment
TaskFlow v2.3.1 (post-database migration). Affects multiple users — reporter and at least one teammate confirmed. Likely affects all users org-wide.

## Severity: high

## Impact
All users relying on search see inverted results. Active tasks are effectively hidden from search, and archived tasks pollute default results. This undermines core search functionality and could cause users to miss or duplicate work. Workaround exists: users can manually select the opposite filter to get correct results, but this is counterintuitive and unreliable.

## Recommended Fix
1. Inspect the v2.3.1 database migration script for changes to the task status field (look for boolean inversions, renamed columns like is_active → is_archived, or changed enum values). 2. Check the search index mapping and query-time filter logic to see which value represents 'active' vs 'archived'. 3. Fix the mismatch — either correct the indexed values via a reindex, or update the query filter predicate to match the new schema semantics. 4. Trigger a full reindex of task status flags after the fix to ensure consistency.

## Proposed Test Case
Create an active task and an archived task. Perform a search with 'Active only' filter and assert the active task is returned and the archived task is not. Repeat with 'Archived' filter and assert the inverse. Run this test against both the old and new schema to catch regressions.

## Information Gaps
- Exact search engine technology (Elasticsearch, PostgreSQL full-text, etc.) — discoverable from codebase
- Specific migration script contents and which column/field changed — discoverable from v2.3.1 changelog or migration files
- Whether any other indexed fields beyond active/archived status were also affected by the migration
