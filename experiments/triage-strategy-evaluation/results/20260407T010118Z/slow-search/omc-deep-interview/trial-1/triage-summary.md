# Triage Summary

**Title:** Description keyword search regressed to ~10-15s in v2.3 (likely missing or dropped full-text index)

## Problem
Since upgrading to v2.3, keyword searches that target task descriptions consistently take 10-15 seconds to return results and cause high CPU usage. Title-only searches remain fast. The slowness is independent of search term frequency or specificity.

## Root Cause Hypothesis
The v2.3 update likely removed, broke, or failed to migrate a full-text index on the task description column (or switched the search implementation to a non-indexed path such as a LIKE/regex scan). The uniform latency regardless of term rarity and the CPU spike are consistent with a sequential scan over all ~5,000 task description blobs rather than an index lookup.

## Reproduction Steps
  1. Install TaskFlow v2.3 desktop app on Linux (Ubuntu, tested on ThinkPad T14)
  2. Populate the database with ~5,000 tasks that have non-trivial description text
  3. Perform a keyword search using the search bar that targets descriptions
  4. Observe ~10-15 second response time and elevated CPU usage
  5. Compare with the same dataset on v2.2 to confirm the regression

## Environment
TaskFlow v2.3 desktop app, Ubuntu Linux, ThinkPad T14, ~5,000 tasks with detailed descriptions

## Severity: high

## Impact
Any user with a significant number of tasks who relies on description search is affected. The reporter uses it multiple times daily to locate meeting notes and context stored in descriptions. Title-only search is a partial workaround but insufficient for their workflow.

## Recommended Fix
Diff the search/query layer between v2.2 and v2.3 for changes to how description search is executed. Check whether a full-text index on the description column exists in v2.3 databases (and in fresh installs vs. upgrades). If the index was dropped or the query path changed, restore indexed full-text search. Run EXPLAIN/query plan analysis on the description search query to confirm whether it's doing a sequential scan.

## Proposed Test Case
Create a performance regression test: populate a database with 5,000+ tasks with realistic description text, execute a description keyword search, and assert the query completes in under 2 seconds. Run this test against both v2.2 and v2.3 schemas to catch index regressions.

## Information Gaps
- Exact database engine and schema used by the desktop app (SQLite, etc.) — dev team can inspect directly
- Whether the regression also affects fresh v2.3 installs or only v2.2→v2.3 upgrades (migration issue vs. code change)
- v2.3 changelog entries related to search or database schema changes
