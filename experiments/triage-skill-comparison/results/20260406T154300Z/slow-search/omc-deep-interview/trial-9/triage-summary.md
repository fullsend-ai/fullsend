# Triage Summary

**Title:** Description search regression in v2.3: 10-15s latency on datasets with many/large tasks

## Problem
Since upgrading to v2.3, searching across task descriptions takes 10-15 seconds consistently. Title-based search remains fast. The reporter has ~5,000 tasks accumulated over 2 years, many with lengthy descriptions (pasted meeting notes). The slowdown is consistent across all description searches regardless of query or session timing.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in description search — most probably a dropped or changed full-text index on the task descriptions column, a switch from indexed search to sequential scan, or a new search implementation (e.g., moving from database-level FTS to application-level string matching) that doesn't scale with large/long description fields.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Populate the database with ~5,000 tasks, including many with long descriptions (multiple paragraphs)
  3. Perform a search by task title — confirm it returns quickly
  4. Perform a search across task descriptions for a known term — observe 10-15 second latency
  5. Repeat the description search to confirm the slowdown is consistent (not a cold-cache issue)

## Environment
TaskFlow v2.3, work laptop (exact OS/specs not gathered but unlikely to be the factor given the version-correlated onset), ~5,000 tasks with many long descriptions

## Severity: medium

## Impact
Power users with large task databases experience severe search latency when searching descriptions. Title search is unaffected. No data loss or functional breakage — search still returns correct results, just slowly. Users who rely on description search for daily workflow are meaningfully impacted.

## Recommended Fix
Compare the search implementation between v2.2 and v2.3 — specifically examine changes to description search queries, full-text indexes, and any ORM/query builder changes. Check whether a database migration in v2.3 dropped or altered an index on the descriptions column. Profile the description search query with EXPLAIN/ANALYZE on a dataset of ~5,000 tasks with large descriptions to confirm whether it's doing a sequential scan.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks (descriptions averaging 500+ words), execute a description search, and assert the query completes in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and later verify the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to matter given version-correlated onset)
- Whether the v2.3 changelog mentions any search or database migration changes
- Whether other users on v2.3 with smaller datasets also experience the slowdown (threshold question)
