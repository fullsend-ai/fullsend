# Triage Summary

**Title:** Memory leak in v2.3 real-time notification system: WebSocket connections/event listeners never cleaned up

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage climbs steadily from ~500MB to 4GB+ over the course of a day, causing progressive slowdown (10+ second page loads, API timeouts) and requiring a daily restart. The issue affects all 200 active users by late afternoon.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature registers WebSocket connections or server-side event listeners for each user action or session but never tears them down when users disconnect, close tabs, or go idle. This causes unbounded accumulation — tens of thousands of leaked connections/listeners per day for 200 active users — leading to memory exhaustion and degraded performance.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server (the reporter's VM or a test instance)
  2. Connect ~200 users (or simulate with a load tool) who keep the app open in browser tabs
  3. Have users perform normal activity (creating tasks, commenting, updating statuses) over several hours
  4. Monitor memory usage and WebSocket/event listener count via Grafana or server metrics
  5. Observe that memory and connection count climb steadily and never reclaim, even as users close tabs or go idle
  6. Compare against the same scenario on v2.2 to confirm the regression

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2 one week ago). ~200 active users who keep TaskFlow open throughout the workday. Same VM, configuration, and user count as before the upgrade. Grafana monitoring in place.

## Severity: high

## Impact
All 200 users experience progressively degraded performance throughout the day, reaching unusable levels (10+ second loads, API timeouts) by late afternoon. Requires daily manual restart, creating a recurring outage window and operational burden.

## Recommended Fix
Investigate the real-time notification subsystem introduced or modified in v2.3. Specifically: (1) Trace the WebSocket or SSE connection lifecycle — look for missing cleanup on disconnect, tab close, or idle timeout events. (2) Check for server-side event listener registration (e.g., pub/sub subscriptions, in-memory listener arrays) that are added per-connection but never removed. (3) Verify that the server handles the browser 'beforeunload' / WebSocket 'close' events and tears down all associated resources. (4) Diff the v2.3 notification code against v2.2 to identify what changed.

## Proposed Test Case
Create an integration test that opens N WebSocket connections to the notification endpoint, performs some actions, then closes all connections. Assert that after a short grace period, the server's tracked connection/listener count returns to zero and memory usage does not grow proportionally with the number of historical connections. Run this in a loop to simulate a full day's connection churn.

## Information Gaps
- Exact server runtime (Node.js, Python, Java, etc.) and notification transport (WebSocket, SSE, long-polling) — not critical since the v2.3 diff will reveal this
- Whether the leak is in connection objects, event listeners, or both
- Server-side error logs that might show failed cleanup attempts
