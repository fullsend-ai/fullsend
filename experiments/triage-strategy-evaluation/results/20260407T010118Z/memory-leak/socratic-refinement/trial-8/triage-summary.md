# Triage Summary

**Title:** Memory leak in real-time notification system introduced in v2.3 causes progressive server degradation

## Problem
After upgrading to v2.3, the TaskFlow server's memory grows linearly from ~500MB to 4GB+ over the course of a day, causing page loads to exceed 10 seconds and API timeouts. Memory never reclaims without a full server restart, even when connected users decrease.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' change is likely not cleaning up WebSocket/SSE connections or their associated event subscriptions and listeners when clients disconnect or navigate away. Each interaction allocates resources (connection handlers, notification subscriptions, event listener callbacks) that are never freed, causing monotonic memory growth proportional to cumulative traffic rather than concurrent connections.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a self-hosted instance
  2. Start the server and note baseline memory (~500MB)
  3. Have users connect and interact normally — create/update tasks, leave comments, with real-time notifications enabled
  4. Monitor memory over several hours; observe steady linear growth
  5. Note that memory does not decrease when users disconnect or go idle
  6. Compare with v2.2 under the same workload to confirm the regression

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2 approximately one week ago), ~200 active users, most keeping TaskFlow open in browser tabs all day, heavy use of real-time notifications

## Severity: high

## Impact
All 200 users on this instance experience progressively degraded performance daily, with the application becoming effectively unusable by late afternoon. Requires manual daily restart as a workaround.

## Recommended Fix
Diff the real-time notification code between v2.2 and v2.3. Look for: (1) WebSocket or SSE connection handlers that don't clean up on disconnect/close events, (2) event listener or subscription registrations without corresponding removal on connection teardown, (3) in-memory data structures (maps, arrays, caches) tracking connected clients or pending notifications that grow without bounds. Add proper cleanup in connection close/error handlers and consider a periodic sweep for stale entries.

## Proposed Test Case
Write an integration test that simulates N clients connecting, subscribing to task notifications, performing interactions, then disconnecting. After all clients disconnect, assert that memory usage (or the size of internal tracking structures) returns to near-baseline levels. Run this in a loop to verify no cumulative growth over multiple connect/disconnect cycles.

## Information Gaps
- No heap dump or memory profile to confirm which objects are accumulating
- Exact server runtime and framework (Node.js, Python, etc.) not specified
- Whether the v2.3 notification change used a new library or refactored existing code is unknown
