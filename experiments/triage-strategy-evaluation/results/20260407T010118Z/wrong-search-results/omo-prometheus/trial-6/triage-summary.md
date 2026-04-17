# Triage Summary

**Title:** Search index filter inverted after v2.3.1 migration — active tasks hidden, archived tasks shown

## Problem
After the v2.3.1 update and its associated database migration, the default search returns archived tasks instead of active ones. Active tasks are excluded from results. The underlying database is correct (tasks show the right status when viewed directly), so the issue is isolated to the search index layer.

## Root Cause Hypothesis
The v2.3.1 database migration likely flipped or incorrectly mapped the active/archived status flag in the search index. The search filter that should include active tasks and exclude archived ones is operating on the inverted value — treating 'active' as 'archived' and vice versa. This could be a boolean inversion, a swapped enum mapping, or a migration that wrote the negated value into the index.

## Reproduction Steps
  1. Update to v2.3.1 and allow the database migration to complete
  2. Perform a default search (no filters) for any known active task — it will not appear
  3. Search for a known archived task — it will appear in results
  4. Create a new task, search for it — it will not appear
  5. Archive that task, search again — it will now appear

## Environment
TaskFlow v2.3.1, post-database migration. Reproduced by multiple users (reporter and teammate). Reporter has ~300 archived tasks and ~150 active tasks.

## Severity: critical

## Impact
All users are affected. Default search is functionally broken — it surfaces irrelevant archived tasks and hides the active tasks users are actually looking for. Search is a core workflow; this makes the product unreliable for day-to-day use. No workaround exists short of browsing tasks directly without search.

## Recommended Fix
Investigate the v2.3.1 database migration script for how it writes the active/archived status to the search index. Look for a boolean inversion, swapped enum values, or an inverted WHERE clause in the index-building query. Fix the mapping and re-index all tasks. Verify by confirming active tasks appear and archived tasks are excluded in default search. Also consider adding an automated check or integration test that validates search index consistency after migrations.

## Proposed Test Case
After fix: create an active task and an archived task. Default search should return the active task and exclude the archived one. Additionally, run a consistency check comparing task status in the database against the search index for all tasks — they should match exactly.

## Information Gaps
- Exact migration script contents (developer would inspect this in code)
- Whether the search index uses a separate data store (e.g., Elasticsearch) or queries the DB directly
- Whether any other v2.3.1 features besides search are affected by the same inversion
