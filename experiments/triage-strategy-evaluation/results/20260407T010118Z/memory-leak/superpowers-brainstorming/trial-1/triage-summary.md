# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications causes progressive server slowdown requiring daily restart

## Problem
After upgrading from v2.2 to v2.3, the self-hosted TaskFlow instance leaks memory steadily throughout the day, climbing from ~500MB at startup to 4GB+ by end of day with ~200 active users. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, requiring a daily server restart.

## Root Cause Hypothesis
The real-time notifications feature introduced in v2.3 is leaking per-connection resources — most likely WebSocket connections, event listeners, or subscription objects that are not being cleaned up when users disconnect, navigate away, or let sessions go idle. The strong correlation between active user count and memory growth rate points to a per-connection leak rather than a background process issue.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 (upgrade from v2.2)
  2. Allow ~200 users to use the system normally over a workday
  3. Monitor memory usage over 6-8 hours
  4. Observe steady memory growth correlated with active user count
  5. For faster reproduction: simulate many concurrent users connecting and disconnecting to isolate whether cleanup on disconnect is the issue

## Environment
Self-hosted TaskFlow v2.3 instance, ~200 active users on weekdays, memory monitored via Grafana

## Severity: high

## Impact
All ~200 users experience degraded performance by mid-afternoon and near-unusable service by end of day. Requires daily manual restarts, risking data loss or service disruption.

## Recommended Fix
Investigate the real-time notification system added in v2.3 for resource leaks. Specifically: (1) Check that WebSocket or SSE connections are properly closed and dereferenced on client disconnect/timeout. (2) Verify event listeners and notification subscriptions are removed when sessions end. (3) Look for growing collections (maps, arrays, sets) that accumulate per-user entries without eviction. A heap snapshot comparison between startup and after several hours of use under load should pinpoint the leaking objects.

## Proposed Test Case
Write a load test that simulates 200 users connecting, performing typical actions, and disconnecting repeatedly over a simulated workday. Assert that memory usage after all users disconnect returns to within a reasonable margin of the baseline (e.g., <20% above startup memory). Also test that individual user disconnect properly frees all associated notification resources.

## Information Gaps
- Exact server runtime and version (Node.js, Java, etc.) — but the dev team already knows this
- Whether the notification system uses WebSockets, SSE, or polling — discoverable from v2.3 source
- Whether a rollback to v2.2 fully resolves the issue — likely yes given the timing, but not explicitly confirmed
