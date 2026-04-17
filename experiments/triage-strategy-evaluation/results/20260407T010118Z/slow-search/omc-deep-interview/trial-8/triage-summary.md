# Triage Summary

**Title:** Full-text search on task descriptions regressed to 10-15s response time in v2.3

## Problem
After updating to TaskFlow v2.3, searching by keywords that match task descriptions takes 10-15 seconds in the main search bar. Searching by task title remains fast. The user has ~5,000 tasks accumulated over 2 years and frequently searches description content (e.g., pasted meeting notes).

## Root Cause Hypothesis
v2.3 likely introduced a regression in how description fields are searched — possible causes include a dropped or altered full-text index on the task description column, a query planner change that switched from indexed lookup to sequential scan, or a new search feature (e.g., fuzzy matching) that bypasses the index. The fact that title search remains fast strongly suggests the issue is isolated to the description search path.

## Reproduction Steps
  1. Create or use a workspace with ~5,000 tasks, many with substantive description text
  2. Ensure TaskFlow is running v2.3
  3. Open the main search bar at the top of the app
  4. Search for a keyword that appears in task descriptions but not titles (e.g., a phrase from meeting notes)
  5. Observe 10-15 second delay before results appear
  6. Search for a keyword that matches a task title — observe that this returns quickly

## Environment
TaskFlow v2.3, work laptop (OS/browser not specified), workspace with ~5,000 tasks accumulated over 2 years

## Severity: medium

## Impact
Users who rely on description search (e.g., for meeting notes stored in tasks) experience significant daily friction. Workaround exists — searching by title — but only works when the user remembers the task title. Likely affects any user with a large task count on v2.3.

## Recommended Fix
1. Diff the search query path between v2.2 and v2.3 for description search specifically. 2. Check database indexes on the task description column — run EXPLAIN ANALYZE on the description search query against a 5K+ task dataset. 3. If a full-text index was dropped or altered in a v2.3 migration, restore it. 4. If a new search mode (fuzzy, semantic, etc.) was added, verify it uses an appropriate index or add one.

## Proposed Test Case
Performance test: with a seeded database of 5,000+ tasks with realistic description text, assert that a keyword search matching description content returns results in under 2 seconds. Include this as a regression test gated on the search query plan using an index.

## Information Gaps
- Whether other v2.3 users with large workspaces experience the same slowness (reporter unsure if teammates use TaskFlow)
- Exact browser and OS details
- Whether the issue occurs on the web app, desktop app, or both
- Server-side vs client-side timing (is the delay in the API response or in rendering?)
