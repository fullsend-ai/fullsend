# Triage Summary

**Title:** Full-text search on task descriptions extremely slow (~10-15s) with large dataset since v2.3

## Problem
Searching across task descriptions takes 10-15 seconds for a user with ~5,000 tasks. Title-only searches remain fast. The regression likely began around the upgrade from v2.2 to v2.3 approximately two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely changed how description search is performed — possibly a missing or dropped full-text index on the task descriptions table/field, a change from indexed search to unoptimized LIKE/sequential scan, or a regression in the search query path that bypasses indexing for description fields while title search still uses its index.

## Reproduction Steps
  1. Create or use an account with approximately 5,000 tasks with populated descriptions
  2. Perform a search for a common keyword (e.g., 'quarterly' or 'budget') across all tasks (no project filter)
  3. Observe that results take 10-15 seconds to return
  4. Compare with a title-only search for the same keyword — this should return quickly

## Environment
Ubuntu 22.04, ThinkPad T14 (32GB RAM), TaskFlow desktop app v2.3 (upgraded from v2.2 ~2 weeks ago)

## Severity: medium

## Impact
Users with large task histories experience significant delays when searching task descriptions, degrading core search usability. Title search is unaffected. Likely affects all users with substantial task counts on v2.3.

## Recommended Fix
Diff the search query path between v2.2 and v2.3, focusing on how description search is executed. Check whether a full-text index on task descriptions was dropped or is no longer being used. Profile the description search query against a 5,000-task dataset to confirm whether it's doing a sequential scan. Restore or add proper indexing for description search.

## Proposed Test Case
Performance test: with a dataset of 5,000+ tasks with populated descriptions, assert that a keyword search across descriptions returns results in under 2 seconds. Include a regression test comparing title-search and description-search query plans to ensure both use indexed lookups.

## Information Gaps
- Exact timing of when the slowness started relative to the v2.3 upgrade (reporter is not 100% certain)
- Whether other users on v2.3 with large datasets experience the same slowness
- Server-side vs client-side bottleneck (no network/performance logs available)
