# Triage Summary

**Title:** Search filter predicate inverted since v2.3.1 — 'active only' returns archived tasks and vice versa

## Problem
Since the v2.3.1 patch update (~3 days ago), the search filter for task status is inverted. Filtering for 'active tasks only' returns archived tasks, and filtering for 'archived tasks only' returns active tasks. The underlying data and index are correct — tasks are properly categorized — but the filter logic applies the wrong predicate.

## Root Cause Hypothesis
The v2.3.1 patch introduced a bug that inverted the boolean logic or enum mapping in the search filter's status predicate. Likely a negation error, swapped enum values, or an inverted conditional in the query construction for the task status filter. Diff the search/filter query-building code between v2.3.0 and v2.3.1.

## Reproduction Steps
  1. Update to v2.3.1
  2. Create an active task with a known name (e.g., 'Q2 planning')
  3. Ensure an archived task with the same or similar name exists
  4. Search for 'Q2 planning' using the main search bar with the default filter (active tasks only)
  5. Observe that the archived task appears but the active task does not
  6. Switch the filter to 'archived tasks only'
  7. Observe that the active task appears instead of the archived one
  8. Switch the filter to 'all tasks' and confirm both tasks are present

## Environment
TaskFlow v2.3.1, main search bar, default filters. Reproduced by at least two users (reporter and teammate).

## Severity: high

## Impact
All users performing filtered searches see inverted results. Active tasks are effectively hidden under default search settings, meaning users cannot find their current work through search. This affects core daily workflow for all users on v2.3.1.

## Recommended Fix
Diff the search filter/query construction code between v2.3.0 and v2.3.1. Look for an inverted boolean, swapped enum value, negated condition, or flipped comparison in the status filter predicate. The fix is almost certainly a one-line logic inversion. Verify that the filter values ('active', 'archived') map to the correct query predicates.

## Proposed Test Case
Unit test the search filter query builder: given a filter set to 'active only', assert that the generated query excludes archived tasks and includes active ones. Add a complementary test for 'archived only'. Add an integration test that creates one active and one archived task with the same name, searches with each filter mode, and verifies the correct task appears in each case.

## Information Gaps
- Exact changelog or commit log for v2.3.1 (not available from reporter, but accessible to developers)
- Whether other filter dimensions (priority, assignee, etc.) are also inverted or only status
