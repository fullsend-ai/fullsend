# Triage Summary

**Title:** Description text search regression in v2.3 — 10-15s query time with high CPU usage

## Problem
Since upgrading to v2.3 (~2 weeks ago), searching tasks by description keywords takes 10-15 seconds and causes high CPU usage (laptop fan spins up). Searching by title remains fast. The slowdown is consistent regardless of query terms or number of results returned.

## Root Cause Hypothesis
The v2.3 release likely introduced a change to how description text is searched — most probably a dropped or altered full-text index on the description field, or a code change that bypasses the index (e.g., switching from an indexed query to an in-application substring scan or unindexed LIKE/regex query). The high CPU and independence from result count are consistent with a full table scan over description text.

## Reproduction Steps
  1. Install or upgrade to TaskFlow v2.3
  2. Have a non-trivial number of tasks with descriptions
  3. Perform a text search using keywords that match task descriptions
  4. Observe ~10-15 second response time and elevated CPU usage
  5. Perform the same keyword search restricted to titles only — observe it completes quickly

## Environment
TaskFlow v2.3, reporter's work laptop (specific OS/DB not confirmed but likely irrelevant given this appears to be a code-level regression)

## Severity: high

## Impact
All users performing description text search on v2.3 are likely affected. Search is a core workflow for task management, and 10-15s delays make it effectively unusable for iterative searching.

## Recommended Fix
Diff the description search code path between v2.2 and v2.3. Check for: (1) dropped or altered database indexes on the description column, (2) query changes that bypass full-text indexing (e.g., LIKE '%term%' replacing a full-text search call), (3) new in-application filtering that loads all descriptions into memory. Run EXPLAIN/ANALYZE on the description search query to confirm whether an index is being used.

## Proposed Test Case
Create a dataset with 10,000+ tasks with multi-paragraph descriptions. Benchmark description keyword search and assert it completes in under 1 second. Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and later verify the fix.

## Information Gaps
- Exact number of tasks in the reporter's instance (not critical — slowness is consistent regardless of result count)
- Specific database engine in use (developer can determine from project config)
- Whether other v2.3 users report the same issue (likely yes given it appears to be a code regression)
