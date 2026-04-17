# Triage Summary

**Title:** Description search regression in v2.3: 10-15s latency on workspaces with ~5,000 tasks

## Problem
After upgrading from v2.2 to v2.3, searching through task descriptions takes 10-15 seconds regardless of query complexity. Title search remains fast. The user has approximately 5,000 tasks with substantial description content (meeting notes pasted into tasks). The slowdown is consistent — not affected by cold/warm start or query length.

## Root Cause Hypothesis
The v2.3 release likely introduced a change to how description search is executed — most probably a dropped or altered full-text index on the task descriptions column/field, a change from indexed search to sequential scan, or a new search implementation (e.g., switching search backends) that doesn't handle large text fields efficiently. The fact that title search is unaffected suggests titles are still indexed while descriptions are not, or that description search now takes a fundamentally different code path.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Create or import a workspace with ~5,000 tasks, with non-trivial description content (e.g., multi-paragraph text in descriptions)
  3. Perform a search scoped to task descriptions using a simple one-word query
  4. Observe response time — expected: 10-15 seconds; expected in v2.2: sub-second

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks with meeting-notes-sized descriptions

## Severity: medium

## Impact
Users with large workspaces who search task descriptions experience severe latency. Power users who store meeting notes in task descriptions and rely on description search are most affected. Title search remains a partial workaround but doesn't cover the primary use case.

## Recommended Fix
1. Diff the search-related code and database migrations between v2.2 and v2.3 — look for changes to description indexing, query construction, or search backend. 2. Check whether a full-text index on task descriptions was dropped, altered, or failed to migrate. 3. Run EXPLAIN/query plan analysis on the description search query against a 5,000-task dataset. 4. If an index was dropped, restore it; if the search backend changed, ensure descriptions are properly indexed in the new backend.

## Proposed Test Case
Performance regression test: seed a test database with 5,000 tasks containing realistic description content (200+ words each). Assert that a single-word description search completes in under 2 seconds. Run this test against both v2.2 and v2.3 search code paths to confirm the regression and verify the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the v2.3 correlation)
- Whether other v2.3 users with large workspaces also experience this (would confirm it's not environment-specific)
- Database backend in use (SQLite vs PostgreSQL vs other) — could affect index behavior
