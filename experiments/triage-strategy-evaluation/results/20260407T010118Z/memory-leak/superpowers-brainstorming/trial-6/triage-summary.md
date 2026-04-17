# Triage Summary

**Title:** Memory leak in v2.3 real-time notification system causes progressive slowdown and daily restart requirement

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory steadily throughout the day, climbing from ~500MB at startup to 4GB+ by end of day. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, forcing a daily restart. The issue affects all 200 active users on the instance.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 is likely leaking WebSocket/SSE connections or event listeners. With 200 active users generating connection events throughout the day, connection handlers or notification event listeners are probably being created but not properly cleaned up on disconnect, tab close, or session timeout. This is consistent with the steady linear memory growth pattern visible in Grafana.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 as a Node.js process (systemd service)
  2. Allow active users to connect and use real-time notifications over the course of several hours
  3. Monitor memory usage via process metrics or Grafana
  4. Observe steady memory climb (~3.5GB growth over a full workday with ~200 users)
  5. Compare with v2.2 under identical conditions to confirm the regression

## Environment
TaskFlow v2.3, Node.js, systemd service on Ubuntu 22.04 VM with 8GB RAM, ~200 active users, upgraded from v2.2 (which did not exhibit the issue)

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires daily manual restart to maintain service.

## Recommended Fix
Investigate the real-time notification code introduced or changed in v2.3. Specifically look for: (1) WebSocket or SSE connections not being closed/cleaned up on client disconnect, (2) event listeners accumulating without removal (check for Node EventEmitter 'MaxListenersExceededWarning'), (3) in-memory data structures (subscriber maps, notification queues) that grow per-connection but are never pruned. A heap snapshot comparison between startup and after several hours of use should pinpoint the leaking objects. As a short-term mitigation, consider whether connection idle timeouts or periodic cleanup sweeps can be added.

## Proposed Test Case
Write a load test that simulates 50+ concurrent users opening and closing real-time notification connections over a 1-hour period. Assert that server memory usage remains within a bounded range (e.g., does not grow beyond 200MB above baseline) and that closed connections are fully garbage-collected. Also add a unit test verifying that notification event listeners are removed when a client disconnects.

## Information Gaps
- Exact Node.js version and whether the --max-old-space-size flag is set
- Whether the Grafana dashboard shows any correlation between active WebSocket connection count and memory growth
- Server-side logs showing any warnings (e.g., EventEmitter memory leak warnings)
