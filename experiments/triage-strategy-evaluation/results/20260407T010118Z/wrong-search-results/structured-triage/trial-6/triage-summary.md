# Triage Summary

**Title:** Search/filter inverted: 'Active tasks' filter returns archived tasks and vice versa since v2.3.1

## Problem
The task list filter is reversed — selecting 'Active tasks' displays archived tasks in search results, and selecting 'Archived tasks' displays active tasks. Individual task detail pages correctly show the archived/active status, so the underlying data is correct but the filter/query logic is inverted.

## Root Cause Hypothesis
The v2.3.1 update (released ~3 days ago) likely introduced a regression in the search/filter query logic — most probably a boolean inversion or swapped enum value when filtering by task status (e.g., `is_archived = true` where it should be `false`, or active/archived filter IDs were transposed).

## Reproduction Steps
  1. Go to the Tasks page (defaults to 'Active tasks' filter)
  2. Type 'Q2 planning' (or any known term) in the main search bar and press Enter
  3. Observe that archived tasks appear in results while known active tasks are missing
  4. Switch the filter to 'Archived tasks'
  5. Observe that active tasks now appear in the archived results

## Environment
Chrome ~124-125, Windows 11, TaskFlow v2.3.1. Confirmed by at least two users (reporter and teammate).

## Severity: high

## Impact
All users performing searches or using the task list filter see inverted results. Active tasks are effectively hidden from normal workflow, and archived tasks clutter the default view. This affects core usability for every user on v2.3.1.

## Recommended Fix
Inspect the v2.3.1 diff for changes to the search/filter query logic — specifically the status filter predicate. Look for an inverted boolean condition or swapped enum/constant when translating the UI filter selection ('Active' vs 'Archived') into a database or API query parameter. The task detail page correctly shows status, so the data model is fine — the bug is in the list/search query layer.

## Proposed Test Case
Create one active task and one archived task with overlapping names. Search with the 'Active tasks' filter and assert only the active task is returned. Switch to 'Archived tasks' filter and assert only the archived task is returned.

## Information Gaps
- Exact Chrome version (reporter unsure if 124 or 125 — unlikely to matter for a server/logic-side bug)
- Whether the issue reproduces on other browsers (given the likely server-side root cause, this is low priority)
