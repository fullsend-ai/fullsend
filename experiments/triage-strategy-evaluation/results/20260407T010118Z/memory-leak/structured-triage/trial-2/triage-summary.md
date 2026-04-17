# Triage Summary

**Title:** Memory leak in TaskFlow v2.3 causes progressive server slowdown requiring daily restart

## Problem
A self-hosted TaskFlow instance experiences a consistent daily memory leak, climbing from ~500MB at startup to 4GB+ by end of day. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, forcing a daily server restart. The issue affects approximately 200 active users.

## Root Cause Hypothesis
The memory leak was introduced in the v2.3 upgrade, most likely in the 'improved real-time notifications' feature. A common pattern: real-time notification systems (e.g., WebSocket connections, SSE streams, or in-memory event listeners) can leak if connections or event handlers are not properly cleaned up when users disconnect or sessions expire. With 200 active users, accumulated leaked connections/handlers throughout the day would explain the steady memory growth.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server (Ubuntu 22.04, bare metal, default database configuration)
  2. Allow normal usage by ~200 active users throughout the day
  3. Monitor memory usage via Grafana or system tools
  4. Observe memory climbing steadily from ~500MB at startup to 4GB+ over 8-10 hours
  5. Note progressive page load degradation and eventual API timeouts

## Environment
TaskFlow v2.3 (upgraded from v2.2 approximately one week ago), Ubuntu 22.04, VM with 8GB RAM, bare metal (no Docker), default database backend, no special caching layer, ~200 active users

## Severity: high

## Impact
All 200 active users experience progressively degrading performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires manual daily server restarts, creating a maintenance burden and end-of-day downtime.

## Recommended Fix
Investigate the real-time notifications feature introduced in v2.3. Specifically: (1) Check for WebSocket/SSE connection cleanup on client disconnect, (2) Look for event listener accumulation (missing removeListener/off calls), (3) Check for in-memory caches or subscription registries that grow without eviction, (4) Run the server with --inspect or heap profiling to identify the leaking objects. As a quick verification, compare the notification subsystem code between v2.2 and v2.3.

## Proposed Test Case
Create a load test that simulates 200 users connecting, performing actions that trigger notifications, and disconnecting over a simulated 8-hour period. Assert that memory usage remains within a bounded range (e.g., does not exceed startup memory + a reasonable threshold) and that all notification-related resources (connections, listeners, subscriptions) are properly freed on disconnect.

## Information Gaps
- Exact database backend and version (reporter unsure, using defaults)
- Server-side error logs or stack traces during the slowdown period
- Whether the real-time notifications feature is actively used or could be disabled as a workaround
- Heap dump or profiler output identifying the specific leaking objects
