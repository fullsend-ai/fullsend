# Triage Summary

**Title:** Description search regression in v2.3: 10-15s latency with ~5k tasks

## Problem
After upgrading from v2.2 to v2.3, searching by task description takes 10-15 seconds. Title-only searches remain fast. The user has approximately 5,000 tasks accumulated over two years.

## Root Cause Hypothesis
The v2.3 release likely changed how description search is executed — possible causes include a removed or missing database index on the description column, a switch from indexed/full-text search to a naive LIKE/full-table scan, or a new search implementation that doesn't scale with corpus size. The fact that title search is unaffected suggests the two search paths diverged in v2.3.

## Reproduction Steps
  1. Set up a TaskFlow instance on v2.3 with a dataset of ~5,000 tasks that have populated description fields
  2. Perform a search using a term that matches task descriptions
  3. Observe query latency of 10-15 seconds
  4. Repeat the same search scoped to title only and observe fast response
  5. Optionally downgrade to v2.2 and confirm description search is fast again

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop, ~5,000 tasks

## Severity: high

## Impact
Any user with a non-trivial number of tasks experiences unusable description search performance. This is a core workflow regression since search is a primary navigation mechanism in a task management app.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on the description search query path. Check for removed indexes, changes to query construction (e.g., full-text search replaced with unindexed LIKE), or new ORM/query-builder behavior. Verify that the description column has an appropriate full-text or trigram index. Add query-level performance tests for description search at scale.

## Proposed Test Case
Performance test: seed a database with 5,000+ tasks with realistic descriptions, execute a description search, and assert response time is under 1 second (or an acceptable threshold). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact database engine and version in use (could affect index behavior)
- Whether the v2.3 migration included any schema changes to the tasks table
- Whether the slowdown scales linearly with task count or has a threshold
