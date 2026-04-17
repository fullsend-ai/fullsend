# Triage Summary

**Title:** Full-text search on task descriptions extremely slow after v2.3 upgrade (10-15s, ~5k tasks)

## Problem
After upgrading from v2.2 to v2.3 approximately two weeks ago, searching for words within task descriptions takes 10-15 seconds. Searching by task title remains fast. The user has ~5,000 tasks, many with lengthy descriptions containing pasted meeting notes.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in full-text indexing of task descriptions — either the index was dropped/not migrated, the search query was changed to bypass the index, or description search was switched to a non-indexed scanning approach (e.g., LIKE/regex scan instead of a full-text index lookup). The fact that title search is unaffected points to a change specific to description-field search, not a general query performance issue.

## Reproduction Steps
  1. Set up a TaskFlow instance with ~5,000 tasks, including tasks with lengthy descriptions (multiple paragraphs)
  2. Perform a search for a word known to appear in task descriptions (not titles)
  3. Observe that the search takes 10-15 seconds to return results
  4. Perform a search for a word that appears in task titles
  5. Observe that title search returns results quickly
  6. Optionally: repeat on a v2.2 instance to confirm the regression

## Environment
TaskFlow v2.3, upgraded from v2.2. Running on a work laptop (OS and specs not specified). Dataset: ~5,000 tasks with lengthy descriptions.

## Severity: high

## Impact
Any user with a non-trivial number of tasks who searches by description content experiences major delays. This is a core workflow regression introduced in the current release. Users with large or text-heavy datasets are most affected.

## Recommended Fix
Diff the search query path and database migration scripts between v2.2 and v2.3. Check whether the full-text index on the task descriptions column still exists and is being used by the query planner (e.g., EXPLAIN ANALYZE the search query). If the index was dropped or altered in a migration, restore it. If the query was changed to bypass the index, revert or fix the query. Verify that any new search features in v2.3 (e.g., new search syntax, new fields) didn't inadvertently change the execution plan for description searches.

## Proposed Test Case
Create a test with 5,000+ tasks with multi-paragraph descriptions. Assert that a full-text search on description content returns results within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.

## Information Gaps
- Exact laptop OS and hardware specs (unlikely to be root cause given the v2.3 correlation)
- Whether tag-based or date-filtered searches are also affected
- TaskFlow's database backend (SQLite, PostgreSQL, etc.) — relevant for understanding indexing specifics
- Whether other v2.3 users with smaller datasets also experience slowness (would help confirm the index hypothesis vs. a query-complexity issue)
