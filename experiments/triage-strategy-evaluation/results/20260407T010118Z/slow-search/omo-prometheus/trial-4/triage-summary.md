# Triage Summary

**Title:** Search on task descriptions regressed to ~10-15s with UI freeze after v2.3 update (~5K tasks)

## Problem
After upgrading from TaskFlow 2.2 to 2.3, searching task descriptions takes 10-15 seconds and freezes the UI. Title-only search remains fast. The user has ~5,000 tasks with lengthy descriptions (meeting notes). The slowdown is consistent regardless of query complexity or whether it is the first or subsequent search.

## Root Cause Hypothesis
v2.3 likely introduced a change to description search that either (a) removed or broke a full-text index on the description field, falling back to a sequential scan, or (b) moved description search from an async/worker-thread operation to a synchronous main-thread operation. The UI freeze and CPU spike confirm the work is blocking the main thread. The large average description size (meeting notes) amplifies the cost of any unindexed scan.

## Reproduction Steps
  1. Install TaskFlow v2.3 as a desktop app
  2. Have or import ~5,000 tasks, ideally with lengthy descriptions (several paragraphs each)
  3. Perform a search that targets task descriptions (not title-only)
  4. Observe ~10-15 second delay with UI freeze and elevated CPU usage
  5. Repeat with a title-only search to confirm it remains fast

## Environment
TaskFlow v2.3 desktop app, work laptop (specific OS not provided), ~5,000 tasks with long descriptions, upgraded from v2.2

## Severity: high

## Impact
Any user with a large task database and description-heavy tasks is affected. The UI freeze makes the app appear hung during search, degrading usability significantly. No known workaround other than limiting searches to titles.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3. Specifically check: (1) whether a full-text index on the description column was dropped or altered in a migration, (2) whether description search was moved from a background/worker thread to the main/UI thread, (3) whether query execution changed (e.g., LIKE scan replacing an FTS query). Restore indexed full-text search on descriptions and ensure the search operation runs off the main thread to prevent UI blocking.

## Proposed Test Case
Create a dataset of 5,000+ tasks with descriptions averaging 500+ words. Run a description search and assert it completes in under 2 seconds without blocking the UI thread (main thread should remain responsive to input events during search execution).

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop
- Exact TaskFlow 2.3 patch version (2.3.0 vs 2.3.x)
- Whether the v2.3 release notes mention any search or database migration changes
- Whether other users on v2.3 with smaller datasets also experience slowness (threshold analysis)
