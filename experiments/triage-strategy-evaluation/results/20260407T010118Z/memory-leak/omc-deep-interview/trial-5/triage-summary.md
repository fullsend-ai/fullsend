# Triage Summary

**Title:** Memory leak in v2.3 real-time notification pathway causes progressive server degradation

## Problem
The TaskFlow application server exhibits a steady memory leak, climbing from ~500MB at startup to 4GB+ over a workday, causing page loads >10s and API timeouts. The issue began coinciding with the upgrade from v2.2 to v2.3 approximately one week ago and requires a daily server restart as a workaround.

## Root Cause Hypothesis
The real-time notification (WebSocket) subsystem reworked in v2.3 is accumulating objects in memory on a per-notification basis. WebSocket connections themselves open and close correctly, but something in the notification processing path — likely notification payloads, event listener registrations, serialized message buffers, or subscription tracking structures — is being retained and not garbage collected after delivery. The leak rate correlates with user activity volume, not connection count.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with default configuration and realtime_notifications enabled
  2. Connect to a PostgreSQL database with default connection pooling
  3. Simulate sustained activity from ~150-200 concurrent users generating notifications over several hours
  4. Monitor application server memory — expect steady linear growth correlating with notification throughput
  5. Compare against the same workload on v2.2 to confirm the regression

## Environment
TaskFlow v2.3, self-hosted on Ubuntu 22.04, 8GB RAM VM, PostgreSQL with default connection pooling, ~200 active users, ~150-180 concurrent WebSocket connections during peak hours

## Severity: high

## Impact
All 200 active users experience progressive performance degradation daily, with the application becoming effectively unusable by late afternoon. Currently mitigated by daily evening restarts. No data loss, but significant productivity impact across the entire user base.

## Recommended Fix
Diff the real-time notification subsystem between v2.2 and v2.3 — focus on notification dispatch, event listener lifecycle, message serialization/buffering, and any new in-memory caching or subscription tracking introduced in the rework. Look for objects retained after notification delivery: uncleared listener arrays, growing maps/caches without eviction, or closures capturing large scopes. A heap dump (reporter is capturing one) will pinpoint the exact object type accumulating. As an immediate workaround, users can set `realtime_notifications: false` to fall back to polling.

## Proposed Test Case
Run a load test simulating 200 users with continuous notification activity over 8 simulated hours. Assert that application server RSS memory stays within a bounded range (e.g., does not exceed startup memory + 500MB) and that no single object type's instance count grows monotonically over the test duration.

## Information Gaps
- Heap dump has not yet been captured — would pinpoint the exact leaking object type and code path
- The notification-disabled confirmation test has not yet been run — would definitively confirm the notification subsystem as the sole source
- Exact TaskFlow runtime (Node.js, Go, etc.) is unknown to the reporter, which slightly affects debugging instructions
