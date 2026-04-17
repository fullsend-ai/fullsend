# Triage Summary

**Title:** Search by task description is extremely slow (~10-15s) with large task count since v2.3

## Problem
Searching tasks by description takes 10-15 seconds for a user with ~5,000 tasks. Searching by title remains fast. The slowness appears to have started around the upgrade from TaskFlow 2.2 to 2.3 approximately two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely changed how description searches are executed — possibly a missing or dropped database index on the task descriptions column, a switch from indexed full-text search to unindexed LIKE/ILIKE queries, or a regression in the search query path that causes a full table scan on descriptions while titles retain their index.

## Reproduction Steps
  1. Create or use an account with approximately 5,000 tasks (with non-trivial description text)
  2. Use the search box to search for a common keyword (e.g., 'budget' or 'meeting notes')
  3. Observe that when the search includes task descriptions, results take 10-15 seconds
  4. Compare with a title-only search for the same keyword — this should return quickly

## Environment
Ubuntu 22.04, ThinkPad T14, Firefox (version unknown), TaskFlow 2.3 (upgraded from 2.2 ~2 weeks ago)

## Severity: medium

## Impact
Users with large task histories experience unacceptable search latency when searching by description. Title-only search is unaffected. This degrades the core search workflow for long-time power users.

## Recommended Fix
1. Diff the search query logic between v2.2 and v2.3 — look for changes to how description search is performed. 2. Check whether a database index on the descriptions column exists and is being used (run EXPLAIN ANALYZE on the search query). 3. If full-text search was replaced or modified in v2.3, restore or add proper indexing for description content. 4. Load-test with 5,000+ tasks to verify the fix brings search latency to acceptable levels.

## Proposed Test Case
Performance test: seed a test database with 5,000 tasks with realistic description text. Execute a description search for a keyword present in ~50 tasks. Assert that results are returned in under 1 second. Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.

## Information Gaps
- Exact Firefox version (unlikely to matter if this is a server-side/database issue)
- Whether the slowness definitively started with the v2.3 upgrade or coincidentally around that time
- Server-side logs or query timing data to confirm the bottleneck is in the database query rather than client-side rendering
