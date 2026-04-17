# Triage Summary

**Title:** Description search regression in v2.3: full-text search across task descriptions causes 10-15s UI freeze

## Problem
After upgrading from TaskFlow 2.2 to 2.3, searching task descriptions takes 10-15 seconds with high CPU usage and UI freezes. Title-only search remains fast. The user has approximately 5,000 tasks, some with very long descriptions containing pasted meeting notes. The slowdown occurs regardless of the search query, as long as descriptions are being searched.

## Root Cause Hypothesis
The 2.2 → 2.3 update likely introduced a regression in description search — most probably removed or broke a full-text index on the description field, or switched from indexed search to a naive in-memory or unindexed scan. The CPU-bound behavior (fan, UI freeze) suggests the search is running synchronously on the main thread, iterating over all description text without an index. Title search remaining fast supports this, as the title field likely retained its index or is small enough to scan quickly.

## Reproduction Steps
  1. Create or use a TaskFlow instance with a large number of tasks (~5,000)
  2. Ensure some tasks have lengthy descriptions (e.g., pasted meeting notes)
  3. Run TaskFlow v2.3
  4. Perform a search that includes task descriptions (not title-only)
  5. Observe 10-15 second delay with high CPU usage and UI freeze
  6. Compare by performing a title-only search — should return quickly

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS not specified), ~5,000 tasks accumulated over 2 years, some with very long descriptions

## Severity: high

## Impact
Any heavy user with a large task database is affected whenever they search descriptions. The UI freeze makes the app unusable during search, disrupting workflow for power users who rely on search.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on how description fields are queried. Check whether a full-text index on descriptions was dropped, altered, or bypassed. If search now runs unindexed or on the main/UI thread, restore the index and move search to a background thread or worker. Consider adding pagination or result-count limits to prevent unbounded scans.

## Proposed Test Case
Performance test: with a dataset of 5,000+ tasks (including tasks with descriptions >1KB), assert that a description search completes in under 2 seconds. Regression test: verify that the search query plan uses an index on the description field rather than a sequential scan.

## Information Gaps
- Exact v2.3 patch version (e.g., 2.3.0 vs 2.3.1)
- Operating system and hardware specs of the reporter's laptop
- Whether the search is implemented client-side (SQLite/local DB) or hits a server
- Database file size on disk
