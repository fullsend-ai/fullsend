# Triage Summary

**Title:** Search active/archived filter is inverted since v2.3.1 database migration

## Problem
Since the v2.3.1 update (approximately 3 days ago), the search filter for active vs. archived tasks is inverted. Default search (which should return active tasks) returns archived tasks instead, and the archived filter returns active tasks. This affects all searches for all users.

## Root Cause Hypothesis
The database migration included in v2.3.1 likely inverted a boolean flag or enum value that distinguishes active from archived tasks. For example, a migration may have swapped the semantics of a column (e.g., `is_active` vs `is_archived`), flipped a boolean default, or reversed an enum mapping, causing the search query's filter predicate to select the wrong set of tasks.

## Reproduction Steps
  1. Log in to TaskFlow on a v2.3.1 instance with a mix of active and archived tasks
  2. Perform a search using the default filter (active tasks only)
  3. Observe that results contain archived tasks instead of active ones
  4. Switch the filter to show archived tasks
  5. Observe that results now contain active tasks instead of archived ones

## Environment
TaskFlow v2.3.1 (post-database migration). Confirmed on at least two user accounts. No browser or OS specifics needed — this is a backend data/query issue.

## Severity: high

## Impact
All users performing searches see inverted results. Active tasks are effectively hidden from default search, making the core search functionality unreliable. Users with many archived tasks (reporter has 300) are most visibly affected.

## Recommended Fix
Examine the database migration scripts in the v2.3.1 release. Look for changes to the column or flag that marks tasks as active vs. archived (e.g., a boolean `is_archived`, status enum, or similar). The migration likely inverted the value. Fix options: (1) write a corrective migration to flip the values back, or (2) if the schema change was intentional, update the search query's filter predicate to match the new semantics.

## Proposed Test Case
Create a test with a known set of active and archived tasks. Execute a search with the 'active only' filter and assert that only active tasks are returned. Execute with the 'archived' filter and assert only archived tasks are returned. Run this test against both the pre- and post-migration schema to confirm the fix.

## Information Gaps
- Exact migration script contents from v2.3.1 (available in codebase, not from reporter)
- Whether the migration was intentionally changing the schema semantics or was a bug in the migration itself
