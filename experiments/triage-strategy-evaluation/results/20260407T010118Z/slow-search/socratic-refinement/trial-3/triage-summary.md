# Triage Summary

**Title:** Description search regression in v2.3: 10-15s query times on large text fields

## Problem
After upgrading from TaskFlow v2.2 to v2.3, full-text search against task descriptions became extremely slow (10-15 seconds), while title search remains fast (<1 second). The results are correct but the latency is unacceptable. High CPU usage is observed during these searches.

## Root Cause Hypothesis
The v2.3 release likely changed the description search implementation — most probably a dropped or missing full-text index on the description field, a switch from indexed search to sequential scan / LIKE-based matching, or a change in the search library/query that doesn't handle large text fields efficiently. The fact that title search is unaffected suggests the regression is specific to description-field indexing or query path.

## Reproduction Steps
  1. Install TaskFlow v2.3 (or upgrade from v2.2)
  2. Populate the database with ~5,000 tasks, some with descriptions of 2,000+ words
  3. Search for a keyword that appears in task descriptions (not just titles)
  4. Observe query time of 10-15 seconds and elevated CPU usage
  5. Compare: search for the same keyword by title only — should return in <1 second
  6. Optionally: downgrade to v2.2 and repeat to confirm the regression

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (specific OS/specs unknown but not likely relevant given it's a regression), ~5,000 tasks accumulated over 2 years, some with 2,000+ word descriptions (pasted meeting notes)

## Severity: high

## Impact
Any user with a substantial task history who searches by description content will experience severe slowdowns. Users with long-form descriptions (meeting notes, etc.) are most affected. This is a regression from previously-working functionality in a core feature.

## Recommended Fix
Diff the search-related code and database migrations between v2.2 and v2.3. Specifically check: (1) whether a full-text index on the description column was dropped or not created in a migration, (2) whether the search query changed from indexed full-text search to unindexed pattern matching (e.g., LIKE '%term%'), (3) whether a search library was swapped or its configuration changed. Run EXPLAIN/ANALYZE on the description search query to confirm whether an index is being used. Restore or add the appropriate full-text index.

## Proposed Test Case
Create a performance/regression test that populates the database with 5,000+ tasks (a subset with descriptions >1,000 words), executes a description keyword search, and asserts the query completes in under 2 seconds. This test should run against new releases to catch future search regressions.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to matter given it's a regression)
- Whether other users on v2.3 experience the same issue (likely yes, given it's a code/schema change)
- The specific database backend in use (SQLite vs PostgreSQL, etc.) — relevant to the fix but discoverable from the codebase
