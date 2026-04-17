# Triage Summary

**Title:** Search regression in v2.3: description-field searches take 10-15 seconds (title searches unaffected)

## Problem
After updating to v2.3, keyword searches that match against task descriptions take 10-15 seconds to return results. Searches that match against task titles remain fast. The user has ~5,000 tasks, many with long descriptions containing pasted meeting notes. This was not an issue in v2.2 — description searches returned almost instantly.

## Root Cause Hypothesis
The v2.3 update likely introduced a regression in how description fields are searched — most probably a dropped or unused full-text index on the description column, a change from indexed search to sequential scanning, or a query planner regression. The fact that title searches are fast suggests the title index is intact while the description search path changed.

## Reproduction Steps
  1. Set up a TaskFlow instance on v2.3 with a large dataset (~5,000 tasks), including tasks with long description fields
  2. Search for a keyword that appears only in a task title — observe fast response
  3. Search for a keyword that appears only in a task description (not the title) — observe 10-15 second delay
  4. Optionally repeat on v2.2 to confirm the regression

## Environment
TaskFlow v2.3, work laptop (OS unspecified), ~5,000 tasks accumulated over 2 years, many tasks with long descriptions containing pasted meeting notes

## Severity: high

## Impact
Any user with a non-trivial number of tasks who searches for content in task descriptions experiences severe slowdowns, effectively breaking a core workflow. The problem scales with dataset size and description length.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3. Specifically investigate: (1) whether a full-text index on the description field was dropped or is no longer being used, (2) whether the search query was refactored in a way that bypasses indexing (e.g., switching from indexed FTS to LIKE/ILIKE scanning), (3) whether a migration in v2.3 failed to rebuild or maintain the description search index. Check query execution plans for description-matching searches.

## Proposed Test Case
Create a performance regression test: seed a database with 5,000+ tasks (some with long descriptions >1KB), then assert that a keyword search matching only description content returns results within an acceptable threshold (e.g., <1 second). Run this test against both v2.2 and v2.3 to confirm the regression and validate any fix.

## Information Gaps
- Exact database backend in use (SQLite vs PostgreSQL vs other)
- Whether the user is on desktop app or web version
- Specific OS and hardware details of the work laptop
- Whether other v2.3 users have reported the same issue
