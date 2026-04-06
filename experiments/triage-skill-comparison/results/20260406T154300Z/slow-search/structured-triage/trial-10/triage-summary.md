# Triage Summary

**Title:** Description search extremely slow (~10-15s) after upgrade to v2.3 with large task count

## Problem
Searching by keywords that appear in task descriptions takes 10-15 seconds to return results, while searching for keywords in task titles returns almost instantly. The reporter has approximately 5,000 tasks and the slowness began around the time they upgraded from v2.2 to v2.3 approximately two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in how description search is executed — possibly a missing or dropped database index on the task description column, a change from indexed full-text search to unindexed LIKE/substring scanning, or a query planner change that causes a full table scan on descriptions but not titles.

## Reproduction Steps
  1. Have an account with a large number of tasks (~5,000)
  2. Run TaskFlow v2.3 (desktop app)
  3. Search for a keyword that appears in task descriptions but not in titles
  4. Observe that results take 10-15 seconds to return
  5. Search for a keyword that appears in task titles
  6. Observe that results return almost instantly

## Environment
TaskFlow v2.3 (desktop app), Ubuntu 22.04, ThinkPad T14, ~5,000 tasks, upgraded from v2.2 approximately two weeks ago

## Severity: high

## Impact
Any user with a substantial number of tasks who searches by description content experiences severe performance degradation, making the search feature effectively unusable for its primary purpose. This is a regression from v2.2.

## Recommended Fix
Compare the v2.2 and v2.3 database schema and search query logic. Check for missing indexes on the task description column or changes to the search query (e.g., switching from full-text search to LIKE queries). Profile the description search query against a dataset of ~5,000 tasks to confirm the bottleneck. Restore or add appropriate indexing for description search.

## Proposed Test Case
Create a test dataset with 5,000+ tasks containing varied description text. Execute a description keyword search and assert that results are returned within an acceptable threshold (e.g., under 1 second). Verify that both title and description searches scale similarly with large task counts.

## Information Gaps
- No error messages or logs were collected, though this is a performance issue rather than an error condition
- Exact query mechanism (full-text search vs. substring match) used in v2.2 vs. v2.3 is unknown without checking the codebase
- Whether other users with large task counts on v2.3 experience the same issue is unconfirmed
