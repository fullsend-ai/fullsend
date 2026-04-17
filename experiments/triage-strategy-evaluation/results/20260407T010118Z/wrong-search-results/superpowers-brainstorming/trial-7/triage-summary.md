# Triage Summary

**Title:** Search filter for archived/active tasks is inverted since v2.3.1

## Problem
The search filter that controls whether archived or active tasks are shown is working backwards. When set to 'active tasks only' (the default), search returns archived tasks. When set to include archived tasks, it returns active ones instead. This affects all searches for at least two confirmed users and began immediately after the v2.3.1 update.

## Root Cause Hypothesis
A logic inversion was introduced in v2.3.1 — most likely a flipped boolean, negated condition, or swapped enum mapping in the search filter code that determines which task status to include/exclude.

## Reproduction Steps
  1. Update to v2.3.1
  2. Create or identify a known active task (e.g., titled 'Q2 planning')
  3. Search for that task with the default filter (active tasks only)
  4. Observe that archived tasks appear but the known active task does not
  5. Toggle the filter to include archived tasks
  6. Observe that the active task now appears instead

## Environment
TaskFlow v2.3.1 (regression not present before this version)

## Severity: high

## Impact
All users searching for tasks see incorrect results by default — active tasks are hidden and archived tasks are surfaced. This directly undermines core search functionality and likely affects every user on v2.3.1.

## Recommended Fix
Diff the search/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean condition, flipped enum value, or negated predicate in the code path that applies the archived/active filter to search queries. Fix the inversion and verify with the reproduction steps above.

## Proposed Test Case
Given tasks in both active and archived states with overlapping names: (1) verify default search returns only active matches, (2) verify toggling the filter to include archived tasks returns both, (3) verify toggling to archived-only returns only archived matches. Assert filter values map to the correct query predicates.

## Information Gaps
- Whether the issue affects all users globally or only a subset (two confirmed so far)
- Whether the inversion also affects other filter dimensions (e.g., by project, by assignee) or only the archived/active filter
