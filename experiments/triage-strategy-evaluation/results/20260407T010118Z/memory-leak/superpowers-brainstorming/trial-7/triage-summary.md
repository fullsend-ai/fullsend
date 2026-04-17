# Triage Summary

**Title:** Memory leak introduced in v2.3: real-time notification subsystem accumulates memory proportional to request volume

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory throughout the day, climbing from ~500MB at startup to 4GB+ by end of day. Page loads degrade to 10+ seconds and API calls time out, requiring a daily restart. The leak correlates with overall request volume rather than active user count.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' changes likely introduced a per-request resource leak — most probably event listeners, notification handlers, or subscription objects that are registered on each incoming request but never cleaned up. The correlation with request volume (not user count) points to something accumulating per-request rather than per-connection.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with real-time notifications enabled
  2. Simulate sustained traffic from ~200 users across web UI, API, and real-time features
  3. Monitor memory usage over several hours
  4. Observe linear memory growth correlated with request volume
  5. Compare against the same workload on v2.2 to confirm no leak

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2 approximately one week ago), ~200 active users, mix of web UI, API, and real-time notification usage

## Severity: high

## Impact
All ~200 users experience progressively degrading performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires daily manual restarts to maintain service.

## Recommended Fix
Diff the real-time notification subsystem between v2.2 and v2.3. Look specifically for: (1) event listeners or notification handlers registered per-request that are never removed, (2) subscription objects or callback references accumulating in a global/module-level collection, (3) missing cleanup in WebSocket disconnect or request-end hooks. A heap snapshot comparison between startup and after sustained load should pinpoint the leaking object type.

## Proposed Test Case
Run a load test simulating 8 hours of typical usage (~200 concurrent users, mixed workload) against v2.3. Assert that memory usage stays within a bounded range (e.g., does not exceed 2x startup memory). The same test against v2.2 should serve as the passing baseline.

## Information Gaps
- Exact WebSocket connection count over time (reporter does not track this metric)
- Whether disabling real-time notifications alone stops the leak (not tested by reporter)
- Heap dump or profiler output identifying the specific leaking object type
