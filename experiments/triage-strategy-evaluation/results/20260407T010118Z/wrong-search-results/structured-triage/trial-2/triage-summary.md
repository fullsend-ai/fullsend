# Triage Summary

**Title:** Search filter inversion: active/archived flag is flipped since v2.3.1

## Problem
Since updating to v2.3.1 (approximately three days ago), the search filter for active vs. archived tasks is inverted. The default 'active tasks only' filter returns archived tasks, while the 'show archived' filter returns active tasks. This affects all searches for all users.

## Root Cause Hypothesis
The v2.3.1 update likely introduced a boolean inversion in the search filter logic — the active/archived predicate is negated, so 'active' queries the archived flag and vice versa. This could be in the search query builder, the index mapping, or a filter parameter that was flipped during the release.

## Reproduction Steps
  1. Log into TaskFlow (v2.3.1)
  2. Navigate to the main dashboard
  3. Use the top search bar to search for a known term (e.g., 'Q2 planning') with default filters (active tasks only)
  4. Observe that results contain archived tasks and omit known active tasks
  5. Switch the filter to 'show archived'
  6. Observe that active tasks now appear in the results

## Environment
App version v2.3.1 (updated ~3 days ago). Reproduced on Chrome ~124-125 / Windows 11 and Safari / macOS — not browser-specific. User has ~150 active and ~300 archived tasks.

## Severity: high

## Impact
All users are affected. Search is effectively unusable under default settings — users see irrelevant archived tasks and cannot find their active work. Workaround exists (manually toggle to 'show archived' to find active tasks) but is counterintuitive and unreliable for users who don't know about it.

## Recommended Fix
Investigate the search filter logic changed in v2.3.1. Look for a boolean inversion in the active/archived predicate — likely a negation bug (e.g., `!isArchived` replaced with `isArchived`, or a flipped enum/query parameter). Check the search query builder, any ORM filter, or Elasticsearch/database query where the archived status is applied. A diff of the search filter code between v2.3.0 and v2.3.1 should surface the change quickly.

## Proposed Test Case
Create one active task and one archived task with a shared keyword. Search using default filters ('active only') and assert that only the active task appears. Switch to 'show archived' and assert that only the archived task appears. This test should cover both the search endpoint and the UI filter parameter passed to it.

## Information Gaps
- Exact Chrome version (minor; confirmed not browser-specific)
- Whether the issue affects other filter combinations beyond active/archived (e.g., by project, by assignee)
- Server-side logs or query traces showing the actual filter parameters being sent
