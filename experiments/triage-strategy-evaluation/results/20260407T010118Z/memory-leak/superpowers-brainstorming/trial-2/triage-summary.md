# Triage Summary

**Title:** Memory leak in v2.3 causes progressive slowdown requiring daily restart

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server exhibits a steady memory leak — growing from ~500MB at startup to 4GB+ over the course of a business day. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, forcing a daily restart. The pattern is consistent and reproducible across multiple days.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 is likely leaking memory — most probably through WebSocket connections or event listeners that are created per-user/per-session but never cleaned up on disconnect, or an unbounded in-memory notification queue/cache that grows with each event.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a self-hosted instance
  2. Allow ~200 users to use the application normally throughout the day
  3. Monitor memory usage via Grafana or system tools
  4. Observe memory climbing steadily from ~500MB toward 4GB+ over 8-10 hours
  5. Observe page loads degrading to 10+ seconds and API timeouts in the afternoon

## Environment
Self-hosted TaskFlow v2.3, approximately 200 active users, previously stable on v2.2

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout each day, with the application becoming effectively unusable by late afternoon. Requires daily manual restarts.

## Recommended Fix
Diff the v2.2→v2.3 changes, focusing on the real-time notifications subsystem. Look for: (1) WebSocket or SSE connections not being cleaned up on client disconnect, (2) event listeners registered per-connection that are never removed, (3) in-memory notification stores or caches that grow without eviction, (4) subscription objects accumulating without cleanup. Run a heap profile on a test instance under simulated load to confirm which objects are accumulating.

## Proposed Test Case
Simulate 200 concurrent users connecting and disconnecting over a 2-hour period. Assert that memory usage remains within a bounded range (e.g., does not exceed startup memory + 50%) and that heap object counts for notification/connection-related classes do not grow monotonically.

## Information Gaps
- Whether memory grows during off-hours (no active users) or only under active use — would distinguish timer-based leak from per-connection leak
- Specific v2.3 deployment mode (single process, clustered, containerized) — could affect where the leak manifests
- Whether the Grafana dashboard shows any correlation between memory growth rate and concurrent user count
