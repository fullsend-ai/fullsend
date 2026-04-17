# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications causes progressive server degradation requiring daily restart

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory proportional to user activity, climbing from ~500MB to 4GB+ during a business day. Memory is never reclaimed even after users disconnect. The server becomes unusably slow (10s+ page loads, API timeouts) and requires a daily restart.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature is likely accumulating per-session state (event listeners, subscription objects, or notification buffers) that is not released when users disconnect or navigate away. This explains why memory grows with user activity, never shrinks after hours, and was not present in v2.2.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a self-hosted instance
  2. Allow ~200 users to perform normal operations (viewing tasks, updating statuses, adding comments) over a business day
  3. Monitor memory usage — it will climb steadily during active hours
  4. After users disconnect, observe that memory remains at peak and is not reclaimed
  5. Compare with v2.2 under the same usage pattern, which does not exhibit the leak

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2 ~1 week ago), ~200 active users, monitored via Grafana

## Severity: high

## Impact
All 200 users experience progressively degraded performance throughout the day, with the application becoming effectively unusable by late afternoon. Operations team must restart the server daily.

## Recommended Fix
Diff the real-time notification system between v2.2 and v2.3. Look for per-connection or per-session state (subscriber lists, event emitters, notification queues, WebSocket handlers) that is allocated on user connect/activity but never freed on disconnect. Likely candidates: event listener registrations without corresponding removal, in-memory notification buffers that grow unboundedly, or WebSocket connection objects retained after the socket closes. Add proper cleanup on disconnect/session-end and consider bounded data structures for any per-user notification state.

## Proposed Test Case
Write a load test that simulates users connecting, performing actions, and disconnecting in cycles. After a full cycle of connect-use-disconnect, assert that memory returns to baseline (within a reasonable margin). Specifically test that real-time notification subscriptions are cleaned up by checking subscriber counts before and after user disconnect.

## Information Gaps
- Exact WebSocket connection count comparison (morning vs afternoon vs after-hours) — would confirm the hypothesis but doesn't change the investigation direction
- Whether the leak is per-connection, per-event, or per-notification (requires code inspection)
- Full v2.3 changelog beyond 'improved real-time notifications' — other changes could also contribute
