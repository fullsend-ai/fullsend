# Triage Summary

**Title:** Search archive filter is inverted since v2.3.1 — active tasks appear as archived and vice versa in search results

## Problem
Since the v2.3.1 update (~3 days ago), the search feature returns inverted results with respect to the archived/active filter. Searching normally returns only archived tasks while hiding active ones. Toggling the 'show archived' filter flips the behavior, showing active tasks instead. The regular task list and project board display correct archive status — the inversion is isolated to search.

## Root Cause Hypothesis
The database migration or code change in v2.3.1 most likely inverted the boolean logic for the archive filter in the search index or search query layer. Since the primary data store shows correct archive status (task list and project board are fine), the search index was either rebuilt with an inverted flag, or the search query's filter predicate was negated. Look for a WHERE clause, filter parameter, or index mapping related to 'archived'/'is_archived'/'status' in the search path that was changed in v2.3.1.

## Reproduction Steps
  1. Ensure you have at least one active task and one archived task with overlapping keywords (e.g., 'Q2 planning')
  2. Use the search feature to search for that keyword with default filters (no archive toggle)
  3. Observe that the archived task appears in results but the active task does not
  4. Toggle the archive filter on
  5. Observe that the active task now appears and the archived task disappears — the opposite of expected behavior
  6. Confirm that viewing the project board or task list without search shows correct archive status

## Environment
TaskFlow v2.3.1, reported by multiple users (at least two confirmed). Issue began approximately 3 days ago coinciding with the v2.3.1 update and an associated database migration.

## Severity: high

## Impact
All search results are inverted for affected users, making search effectively unusable for its intended purpose. At least two users confirmed affected; likely org-wide. A workaround exists (manually toggling the archive filter to get opposite behavior) but is unintuitive and error-prone.

## Recommended Fix
Inspect the v2.3.1 migration and search-related code changes. Look for an inverted boolean in the search index build/rebuild logic (e.g., storing !is_archived instead of is_archived) or an inverted filter predicate in the search query path. If the search index is separate from the primary data store, rebuilding the index with the corrected flag mapping should fix existing data. The fix is likely a single boolean negation correction plus a reindex.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search for that keyword with the default (non-archived) filter and assert that only the active task appears. Toggle to the archived filter and assert that only the archived task appears. Run this test against both the search path and the direct list/browse path to ensure consistency.

## Information Gaps
- Exact number of affected users — reporter confirmed herself and one teammate, but scope may be org-wide
- Whether all search backends are affected (if TaskFlow uses multiple search providers or full-text vs. filtered search)
- Exact migration script or changeset in v2.3.1 that touched search indexing (developer investigation item)
