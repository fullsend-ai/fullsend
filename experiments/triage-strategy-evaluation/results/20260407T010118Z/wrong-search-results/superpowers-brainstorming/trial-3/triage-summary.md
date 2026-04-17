# Triage Summary

**Title:** Search active/archived filter is inverted since v2.3.1 — active filter returns archived tasks and vice versa

## Problem
The search filter for task status (active vs. archived) is returning inverted results. When set to show active tasks, it returns archived tasks; when set to show archived tasks, it returns active ones. This affects all searches for all users and began after the v2.3.1 update approximately three days ago.

## Root Cause Hypothesis
A logic inversion was introduced in v2.3.1 in the search filtering code — most likely a negated boolean condition, a swapped enum mapping, or an inverted query predicate for the active/archived status filter.

## Reproduction Steps
  1. Update to v2.3.1
  2. Search for a term (e.g., 'Q2 planning') with the default active-tasks filter enabled
  3. Observe that archived tasks appear in results while known active tasks are missing
  4. Toggle the filter to show archived tasks
  5. Observe that active tasks now appear instead

## Environment
TaskFlow v2.3.1 (regression not present in prior version)

## Severity: high

## Impact
All users are affected — the default search experience returns wrong results, hiding active work and surfacing irrelevant archived tasks. This undermines core task-finding functionality.

## Recommended Fix
Diff the search filter logic between v2.3.0 and v2.3.1. Look for the query or predicate that filters on task status (active/archived) — there is likely an inverted condition (e.g., `!isArchived` changed to `isArchived`, or a swapped enum value). Fix the inversion and add a regression test.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search with the active filter enabled and assert the active task is returned and the archived task is not. Then search with the archived filter and assert the reverse. This test should run against both filter states to catch any future inversion.

## Information Gaps
- Whether other search filters (e.g., by assignee, priority, date) are also affected or only the active/archived filter
- Whether the issue is in the frontend filter parameter or the backend query logic
