# Triage Summary

**Title:** Search archive filter inverted after v2.3.1 migration — 'active only' returns archived tasks and vice versa

## Problem
After the v2.3.1 update (approximately 3 days ago), the search feature's active/archived filter is inverted. The default 'active tasks only' filter returns archived tasks while excluding active ones. Selecting the archived filter returns active tasks instead. Other search filters (project, assignee) work correctly. Task display outside of search (task lists, direct navigation) is unaffected.

## Root Cause Hypothesis
The database migration included in v2.3.1 likely inverted the boolean/enum value representing archive status in the search index or search query layer. Since direct task list browsing shows correct status labels, the underlying task data is correct — the bug is isolated to how search queries interpret or join against the archive status field. Possible causes: (1) migration flipped a boolean column (e.g., `is_active` → `is_archived` without inverting values), (2) search index was rebuilt with inverted logic, or (3) a query predicate was changed from `= true` to `= false` or equivalent.

## Reproduction Steps
  1. Log in to TaskFlow on a workspace that has received the v2.3.1 update
  2. Create or confirm an active task with a known title (e.g., 'Q2 planning')
  3. Ensure at least one archived task also exists with a matching or similar title
  4. Use the search feature with the default 'active tasks only' filter
  5. Observe that archived tasks appear in results while the known active task is missing
  6. Toggle the filter to 'archived tasks' and observe that the active task now appears

## Environment
TaskFlow v2.3.1 (post-patch with database migration). Reproduced by at least two users in the same workspace. No specific browser/OS dependency reported — behavior is server-side.

## Severity: high

## Impact
All users are affected. The default search filter is 'active tasks only', meaning every search returns wrong results out of the box. Users see stale archived tasks instead of their current work. Workaround exists (manually invert the filter selection) but is unintuitive and easy to miss.

## Recommended Fix
Investigate the v2.3.1 database migration scripts for changes to archive status fields in the search index or search query logic. Specifically look for: (1) boolean column inversions (e.g., `is_active` renamed to `is_archived` without flipping values), (2) changes to search query predicates that filter on archive status, (3) search index rebuild logic that may have inverted the status mapping. Fix the inversion and verify with a query that `WHERE is_archived = false` (or equivalent) returns only active tasks.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search with 'active tasks only' filter and assert only the active task is returned. Search with 'archived tasks' filter and assert only the archived task is returned. This test should be added as a regression test gating future migrations.

## Information Gaps
- Exact migration script contents from v2.3.1 (dev team can inspect directly)
- Whether the search uses a separate index (e.g., Elasticsearch) or queries the primary DB directly
- Whether any API-level searches (not just UI) exhibit the same inversion
