# Triage Summary

**Title:** Search archive filter inverted since v2.3.1 — 'active tasks only' returns archived tasks and vice versa

## Problem
Since the v2.3.1 patch (~3 days ago), the search archive filter behaves inversely: searching with the default 'active tasks only' filter returns archived tasks, while switching to the archived filter shows active tasks. This affects all searches for all users.

## Root Cause Hypothesis
The v2.3.1 patch likely inverted the boolean logic or enum mapping for the archive status filter in the search query. This could be a negated condition, a swapped enum value, or an inverted boolean flag in the search index query.

## Reproduction Steps
  1. Search for any term (e.g., 'Q2 planning') with the default 'active tasks only' filter
  2. Observe that results include archived tasks and exclude known active tasks
  3. Switch the filter to 'show archived tasks'
  4. Observe that active tasks now appear in results

## Environment
v2.3.1 (patch applied ~3 days ago). Affects multiple users.

## Severity: high

## Impact
All users are affected. Search — a core feature — is functionally broken: users cannot find active tasks without manually inverting the filter, and default search results are misleading.

## Recommended Fix
Diff the search filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean condition, swapped enum/constant, or negated predicate in the archive status filter applied to search queries. The fix is likely a one-line boolean or comparison inversion.

## Proposed Test Case
Create one active task and one archived task with overlapping keywords. Search with 'active only' filter and assert the active task is returned and the archived task is not. Search with 'archived' filter and assert the inverse. This test should be run against both filter states to catch inversions.

## Information Gaps
- Exact code path changed in v2.3.1 (developer will identify from the diff)
