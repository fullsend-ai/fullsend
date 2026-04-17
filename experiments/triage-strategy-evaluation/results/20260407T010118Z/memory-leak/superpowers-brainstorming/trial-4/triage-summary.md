# Triage Summary

**Title:** Memory leak in v2.3: per-request memory accumulation causes server degradation requiring daily restart

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage climbs from ~500MB at startup to 4GB+ over the course of a day, causing page loads to exceed 10 seconds and API timeouts. The server requires a daily restart to recover. The memory growth rate correlates with API request volume, not active connection count.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature likely introduced per-request middleware or event processing that leaks memory on each API request — e.g., accumulating notification payloads, registering event listeners per request without cleanup, or caching request-scoped objects in a module-level data structure. The leak is per-request (not per-connection), since memory growth tracks request volume rather than connected users.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 (upgrading from v2.2)
  2. Allow ~200 users to interact with the system normally over a full workday
  3. Monitor server memory usage via Grafana or process metrics
  4. Observe memory climbing linearly from ~500MB, correlated with API request volume
  5. By end of day, memory reaches 4GB+ and response times degrade to 10+ seconds

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2), approximately 200 active users, server monitored via Grafana

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires daily manual restart as a workaround.

## Recommended Fix
1. Diff the v2.2→v2.3 changes in the real-time notifications system, focusing on any new per-request middleware, event handlers, or caching layers. 2. Take a heap snapshot under load and compare object retention between early-day and late-day to identify the accumulating objects. 3. Look for patterns like: event listeners registered per request but never removed, growing in-memory caches without eviction, notification history accumulating in a module-level array/map, or request-scoped objects captured in closures that prevent GC. 4. Verify the fix by confirming memory remains stable over an extended period under normal load.

## Proposed Test Case
Write a load test that simulates a full day of API traffic (thousands of requests from 200 simulated users) against a v2.3 instance with the notifications feature active. Assert that server RSS memory stays within a bounded range (e.g., does not grow beyond 2x baseline) after N thousand requests. This test should also pass against v2.2 as a control.

## Information Gaps
- Exact server runtime/language (Node.js, Python, etc.) — though the investigation approach is similar regardless
- Whether the real-time notifications feature can be disabled via config flag to confirm it as the source
- Specific heap profile or leak trace identifying the accumulating objects
