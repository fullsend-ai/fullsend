# Triage Summary

**Title:** Full-text search on task descriptions is extremely slow (~10-15s) with large task counts

## Problem
Searching for keywords that appear in task descriptions takes 10-15 seconds to return results, whereas searching for keywords in task titles returns quickly. The reporter has approximately 5,000 tasks and recently upgraded to version 2.3. The issue affects searches across all tasks with no project or filter scoping.

## Root Cause Hypothesis
The description field likely lacks a proper full-text search index, or an existing index was dropped or not migrated during the 2.3 upgrade. Title searches are fast because the title field is indexed. With ~5,000 tasks, an unindexed full-text scan of description content would explain the 10-15 second latency.

## Reproduction Steps
  1. Create or use an account with a large number of tasks (~5,000) that have text content in their descriptions
  2. Open the search feature with no project or filter selected
  3. Search for a keyword that appears in task descriptions but not in titles
  4. Observe that results take 10-15 seconds to return
  5. Search for a keyword that appears in a task title and observe it returns quickly

## Environment
Ubuntu 22.04, ThinkPad T14, TaskFlow desktop app version 2.3 (upgraded approximately two weeks ago)

## Severity: medium

## Impact
Users with large task counts experience unusable search latency when searching description content. Power users who have accumulated thousands of tasks over time are most affected. Core search functionality is degraded but not broken.

## Recommended Fix
Investigate whether a full-text index exists on the task description field in v2.3. Compare the database schema or query plan against v2.2. If the index is missing, add it. If it exists, check the query planner to confirm it is being used. Also check whether the v2.3 upgrade migration dropped or failed to create the index.

## Proposed Test Case
With a dataset of 5,000+ tasks with populated descriptions, run a search for a keyword found only in descriptions and assert the query completes within an acceptable threshold (e.g., under 1 second). Verify the query plan uses an index scan rather than a sequential scan.

## Information Gaps
- Whether the slowness began exactly with the v2.3 upgrade or was already present before
- Desktop app log output during a slow search (could confirm whether latency is server-side or client-side)
- Whether the issue reproduces on the web version or is desktop-app specific
