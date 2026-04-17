# Triage Summary

**Title:** Description search regression in v2.3: 10-15s latency on full-text description queries

## Problem
After upgrading to TaskFlow v2.3, searching across task descriptions takes 10-15 seconds, whereas it was sub-second prior to the upgrade. Title search remains fast. The issue affects workspaces with large numbers of tasks (~5,000) that have lengthy descriptions. Filtering to a smaller project first reduces but does not eliminate the slowness, suggesting the issue scales with description volume rather than task count alone.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in how description search is performed — most probably a change to the search indexing strategy (e.g., full-text index on descriptions was dropped, rebuilt incorrectly, or replaced with a sequential scan), or a change to the query path that bypasses an existing index for description fields. The fact that title search is unaffected suggests title and description searches use different code paths or indexes, and only the description path regressed.

## Reproduction Steps
  1. Install or upgrade to TaskFlow v2.3
  2. Create or import a workspace with ~5,000 tasks, many with long descriptions (multi-paragraph, e.g., pasted meeting notes)
  3. Perform a search by task title — confirm it returns results in under 1 second
  4. Perform a search across task descriptions for any common term
  5. Observe that description search takes 10-15 seconds
  6. Optionally filter to a smaller project first and repeat — slowness is reduced but still present

## Environment
TaskFlow v2.3, work laptop (specific OS/specs not provided but likely not relevant given this is a version-correlated regression), workspace with ~5,000 tasks accumulated over 2 years, many tasks with long descriptions

## Severity: medium

## Impact
Affects users with large workspaces who rely on description search. Title-based search still works as a partial workaround. The feature is functional but severely degraded — not a blocker but a significant productivity hit for power users.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on the description search path. Check for: (1) dropped or altered full-text indexes on the description field, (2) ORM/query changes that bypass indexes, (3) new search features (e.g., fuzzy matching, regex) that inadvertently force sequential scans on descriptions. Run EXPLAIN/query plans on the description search query against a dataset with ~5,000 tasks with long descriptions. If indexing was dropped, restore it; if the query path changed, ensure it uses the index.

## Proposed Test Case
Performance test: seed a test database with 5,000 tasks where at least 1,000 have descriptions > 500 words. Assert that a description search for a common term returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the regression and validate the fix.

## Information Gaps
- Exact database backend in use (SQLite, PostgreSQL, etc.) — may affect index strategy
- Whether v2.3 changelog mentions any search-related changes
- Server-side vs. client-side search architecture
