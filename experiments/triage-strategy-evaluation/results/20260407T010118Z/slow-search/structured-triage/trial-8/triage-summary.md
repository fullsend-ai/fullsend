# Triage Summary

**Title:** Search on task descriptions extremely slow (~10-15s) after upgrade to v2.3 with large dataset

## Problem
Full-text search against task descriptions via the main search bar takes 10-15 seconds to return results, while searches matching task titles return quickly. The reporter has approximately 5,000 tasks and believes the slowness began around the time they upgraded from TaskFlow 2.2 to 2.3 roughly two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in description search — possibly a missing or dropped full-text index on the task descriptions column/field, a query plan change, or a switch from indexed search to unoptimized LIKE/scan-based search for description content. The fact that title searches remain fast suggests the title field still has proper indexing while the description field does not.

## Reproduction Steps
  1. Set up a TaskFlow instance running version 2.3 (desktop app)
  2. Populate the database with ~5,000 tasks that have substantive text in their descriptions
  3. Open the main search bar at the top of the app
  4. Search for a keyword that appears only in task descriptions (not titles)
  5. Observe that the search takes 10-15 seconds to return results
  6. Search for a keyword that appears in a task title and observe it returns quickly

## Environment
Ubuntu 22.04, Lenovo ThinkPad T14, 32GB RAM, TaskFlow v2.3 desktop app (upgraded from v2.2 approximately two weeks ago)

## Severity: medium

## Impact
Users with large task databases (~5,000+ tasks) experience severe search latency on description queries, degrading a core workflow. Likely affects all users at scale on v2.3.

## Recommended Fix
Compare the search query path and indexing for task descriptions between v2.2 and v2.3. Check whether a full-text index on the descriptions field was dropped, altered, or is no longer being used by the query planner. Review the v2.3 migration scripts and any ORM/schema changes that touch the search or descriptions columns.

## Proposed Test Case
Create a test with 5,000+ tasks with varied description text. Assert that a keyword search matching only descriptions completes within an acceptable threshold (e.g., <1 second). Run this test against both v2.2 and v2.3 schemas to confirm the regression.

## Information Gaps
- Exact timing of when slowness started relative to the v2.2→v2.3 upgrade (reporter is not 100% certain)
- Whether any error messages or warnings appear in application logs during slow searches
- Whether the desktop app uses an embedded database (SQLite) or a remote backend — this affects which indexing approach to investigate
