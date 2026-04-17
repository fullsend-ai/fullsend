# Triage Summary

**Title:** Search filter for active/archived tasks is inverted — 'active only' returns archived tasks and vice versa

## Problem
The search filter that controls whether active or archived tasks are shown is producing inverted results. Setting the filter to 'active only' (the default) returns archived tasks, and setting it to 'archived only' returns active tasks. When the filter is disabled entirely (show all), both active and archived tasks appear correctly, confirming the data itself is intact.

## Root Cause Hypothesis
The boolean condition that applies the active/archived filter is inverted. Most likely a negation error (e.g., `!isArchived` where `isArchived` is expected, or a flipped enum comparison) in the search query builder or filter application logic. Since 'show all' works correctly, the underlying data and index are fine — only the filter predicate is wrong.

## Reproduction Steps
  1. Have a user account with both active and archived tasks (e.g., 150 active, 300 archived)
  2. Perform a search with the default 'active only' filter
  3. Observe that archived tasks appear in results while active tasks are missing
  4. Toggle the filter to 'archived only'
  5. Observe that active tasks now appear instead of archived ones
  6. Toggle the filter to 'show all' and confirm both sets appear correctly

## Environment
Affects multiple users (reporter and at least one teammate). No specific browser/OS noted, suggesting a server-side or shared logic issue rather than a client-specific rendering bug.

## Severity: high

## Impact
All users relying on the default search behavior see stale archived tasks instead of their active work. This effectively breaks the primary search workflow for every user, since the default filter is 'active only.'

## Recommended Fix
Locate the filter predicate for active/archived status in the search query path and fix the inverted boolean logic. Check for a negation error, swapped enum values, or an inverted comparison. The fix is likely a one-line change. Also review whether this inversion was introduced by a recent commit (regression) to understand how it shipped.

## Proposed Test Case
Given a dataset with known active and archived tasks: (1) search with 'active only' filter and assert results contain only active tasks, (2) search with 'archived only' filter and assert results contain only archived tasks, (3) search with 'show all' and assert results contain both. These three assertions would have caught the inversion.

## Information Gaps
- Exact code path or recent commit that introduced the inversion (discoverable from version control)
- Whether this affects all search types or only keyword search (but the fix is the same regardless)
