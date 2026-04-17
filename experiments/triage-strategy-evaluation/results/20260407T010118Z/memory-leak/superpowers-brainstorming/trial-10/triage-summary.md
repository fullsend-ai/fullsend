# Triage Summary

**Title:** Memory leak regression in v2.3: per-request memory growth causes server slowdown requiring daily restart

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server exhibits a per-request memory leak. Memory climbs from ~500MB at startup to 4GB+ over the course of a business day, causing page loads to exceed 10 seconds and API timeouts. The leak rate correlates directly with request volume — faster during peak hours, slower on weekends — confirming it is triggered by request handling rather than a background process.

## Root Cause Hypothesis
A code change in v2.3 introduced a per-request memory leak, most likely objects (such as request contexts, database connections, cached query results, or event listeners) being allocated per request but not released for garbage collection. Common culprits include closures capturing request-scoped data in a module-level cache, growing arrays/maps that are never pruned, or event listeners registered per request without removal.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a VM or container with memory monitoring enabled
  2. Run a sustained load test simulating typical user activity (~200 concurrent users or equivalent request rate)
  3. Monitor Node.js heap usage over 1-2 hours
  4. Observe steady memory growth proportional to request count with no plateau
  5. Optionally repeat with v2.2 to confirm the leak is absent in the prior release

## Environment
Ubuntu 22.04 VM, 8GB RAM, TaskFlow v2.3, Node.js (version unspecified), ~200 active users

## Severity: high

## Impact
All 200 active users experience progressively degrading performance throughout the day, culminating in timeouts. The team is forced to restart the server daily as a workaround, causing brief downtime.

## Recommended Fix
Diff the codebase between v2.2 and v2.3, focusing on request-handling middleware, database query paths, and any new caching or event-listener logic. Take a heap snapshot under load using Node.js --inspect and Chrome DevTools (or a tool like clinic.js) to identify which objects are accumulating. Look specifically for: (1) growing Maps/Sets/arrays at module scope, (2) event listeners added per request without cleanup, (3) connection pool or session objects not being released. Once the leaking allocation is identified, ensure proper cleanup on request completion.

## Proposed Test Case
Add a load/soak test that sends N thousand requests over a sustained period and asserts that heap usage after a full GC does not exceed a threshold (e.g., startup memory + 20%). This test should run against both v2.2 (baseline, should pass) and v2.3 (should fail before fix, pass after).

## Information Gaps
- Exact Node.js version running on the server
- Whether any TaskFlow plugins or custom middleware are installed that might contribute
- Which specific endpoints or features are used most heavily (could help narrow the leak source, but the v2.2-v2.3 diff is a more direct path)
