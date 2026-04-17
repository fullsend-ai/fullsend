# Triage Summary

**Title:** Description search regression in v2.3: 10-15s response time with ~5,000 tasks

## Problem
After upgrading from v2.2 to v2.3, searching by task description takes 10-15 seconds. Title-based search remains fast. The user has approximately 5,000 tasks accumulated over two years of use.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in the description search path — most probably a missing or broken database index on the task descriptions column, a change from indexed/full-text search to a naive LIKE/sequential scan, or a new processing step (e.g., snippet generation, highlighting) that scales poorly with description length and task count.

## Reproduction Steps
  1. Set up a TaskFlow instance running v2.3
  2. Populate with ~5,000 tasks that have non-trivial descriptions
  3. Perform a search using a term that matches task descriptions
  4. Observe response time of 10-15 seconds
  5. Perform the same search scoped to titles only — observe fast response
  6. Repeat on v2.2 with the same dataset to confirm regression

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks

## Severity: medium

## Impact
Users with large task databases experience unusable description search times after upgrading to v2.3. Title search is unaffected, providing a partial workaround. Likely affects any user with a non-trivial number of tasks.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, specifically the query path for description search. Check for: (1) missing or dropped database index on description fields, (2) changes from full-text search to unindexed LIKE/ILIKE queries, (3) newly added per-row processing (regex, snippet extraction) that doesn't scale. Run EXPLAIN/EXPLAIN ANALYZE on the description search query against a 5,000-task dataset to identify the bottleneck.

## Proposed Test Case
Performance test: seed database with 5,000+ tasks with realistic descriptions. Assert that a description search query returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores prior performance.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the version correlation)
- Whether the search is backed by a SQL database, full-text engine, or in-memory — affects fix approach
- Whether other v2.3 users with large datasets report the same issue
