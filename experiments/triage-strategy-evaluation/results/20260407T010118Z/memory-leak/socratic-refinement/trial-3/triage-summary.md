# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications causes server degradation requiring daily restart

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage grows unbounded from ~500MB at startup to 4GB+ over an 8-hour business day, causing page loads to exceed 10 seconds and API timeouts. The server must be restarted daily as a workaround. The leak rate correlates with request/activity volume, not with elapsed time or connection count.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 is allocating resources (e.g., event handlers, notification payloads, subscriber lists, or message buffers) on each notification-triggering action that are never freed. Since WebSocket connection counts remain stable while memory climbs, the leak is per-event rather than per-connection — every user action that generates a notification leaves behind a small allocation that accumulates over the day.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server and note baseline memory (~500MB)
  2. Simulate or allow normal business-day activity from ~150-200 concurrent users performing typical actions (creating tasks, updating statuses, checking dashboards)
  3. Monitor server memory via Grafana or similar over several hours
  4. Observe memory climbing steadily in proportion to request volume, reaching 3-4GB after ~8 hours of typical activity
  5. Confirm WebSocket connection counts remain stable and do not grow unbounded
  6. Optionally compare against v2.2 under identical load to confirm the leak is absent

## Environment
Self-hosted TaskFlow v2.3, ~200 active users, single VM (specs not specified), users keep browser tabs open throughout the day. Previously stable on v2.2 for weeks without restart.

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout the workday, with the application becoming effectively unusable by late afternoon. Requires manual daily server restarts, causing downtime and operational burden.

## Recommended Fix
Investigate the real-time notifications code path changed in v2.3. Take a heap dump or memory profile during active use and compare to v2.2. Look for per-event allocations that accumulate: event listener registrations that aren't removed, notification objects stored in an unbounded in-memory collection, subscriber tracking structures that grow per-event rather than per-connection, or message history/buffers that are never pruned. The fix likely involves ensuring per-notification allocations are released after delivery or bounded by a cap/TTL.

## Proposed Test Case
Write a load test that simulates 200 concurrent users performing notification-triggering actions over a sustained period (e.g., 1000 actions per user). Assert that server memory usage remains within a bounded range (e.g., does not exceed 2x baseline) after all actions complete and a GC cycle is triggered. Additionally, add a unit test for the notification subsystem confirming that per-event allocations are released after notification delivery.

## Information Gaps
- Server runtime/language (Node.js, Java, Python, etc.) — determines which profiling tools to use
- Whether disabling the real-time notifications feature (if possible via config) stops the leak — would confirm the hypothesis definitively
- Exact v2.3 changelog diff for the notifications feature
