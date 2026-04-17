# Triage Summary

**Title:** Search filter inverted since v2.3.1: 'Active tasks only' returns archived tasks and vice versa

## Problem
After updating to v2.3.1, the main search bar's status filter is inverted. Selecting 'Active tasks only' returns archived tasks, and selecting 'Archived tasks' returns active tasks. 'All tasks' works correctly but without proper ordering. The issue affects all searches for all users on v2.3.1.

## Root Cause Hypothesis
The v2.3.1 update likely introduced an inverted boolean or swapped enum mapping in the search query's status filter predicate. Since task records store correct status (visible when clicking into a task) and 'All tasks' returns everything, the data layer is intact — the bug is isolated to how the filter selection is translated into the search query. A likely candidate is a negated condition (e.g., `status == 'archived'` where it should be `status == 'active'`) or swapped enum values in the filter-to-query mapping.

## Reproduction Steps
  1. Log in to a TaskFlow instance running v2.3.1 with a mix of active and archived tasks
  2. Use the main search bar on the dashboard with the default 'Active tasks only' filter
  3. Search for a term that matches both active and archived tasks (e.g., 'Q2 planning')
  4. Observe that archived tasks appear in results instead of active ones
  5. Toggle filter to 'Archived tasks' and observe that active tasks now appear
  6. Toggle filter to 'All tasks' and observe that all tasks appear correctly
  7. Click into any result to confirm the task's own status label is correct (data integrity is fine)

## Environment
TaskFlow v2.3.1, main search bar on dashboard, default filters. Confirmed by multiple users. User has ~300 archived and ~150 active tasks. Worked correctly on the version prior to v2.3.1.

## Severity: high

## Impact
All users on v2.3.1 are affected. Search — a core workflow — returns wrong results by default, forcing users to either mentally invert the filter or wade through 'All tasks' unfiltered. Users with many archived tasks (like this reporter with 300) are especially impacted as archived results bury active work.

## Recommended Fix
Diff the search filter/query logic between v2.3.0 and v2.3.1. Look for an inverted condition or swapped enum/constant in the code path that maps the UI filter selection ('Active tasks only' / 'Archived tasks') to the database or API query predicate. The fix is almost certainly a one-line boolean or enum swap. Check for similar inversions in any other status-based filters that may have been touched in the same changeset.

## Proposed Test Case
Add a test with a mix of active and archived tasks that asserts: (1) filtering by 'Active tasks only' returns exactly the active tasks, (2) filtering by 'Archived tasks' returns exactly the archived tasks, (3) filtering by 'All tasks' returns both. This should be a regression test gated on the search query builder.

## Information Gaps
- Exact code change in v2.3.1 that introduced the inversion (requires inspecting the diff)
- Whether other filter types (e.g., by priority, assignee) were similarly affected by the same changeset
