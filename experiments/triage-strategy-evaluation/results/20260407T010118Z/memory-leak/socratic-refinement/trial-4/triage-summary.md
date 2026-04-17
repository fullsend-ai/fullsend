# Triage Summary

**Title:** Memory leak via WebSocket connection accumulation in v2.3 real-time notification system

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server process exhibits a steady memory leak from ~500MB at startup to 4GB+ over a business day, causing severe performance degradation (10+ second page loads, API timeouts) and requiring daily restarts. The leak rate scales linearly with the number of active users.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature is not properly closing or cleaning up WebSocket connections (and/or their associated event listeners) when users navigate away, close tabs, or reconnect. Each user interaction that should reuse or replace an existing connection instead opens additional ones, causing connection count and memory to grow unboundedly throughout the day.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 self-hosted with real-time push notifications enabled
  2. Allow normal usage by ~200 users over a full business day (9am-5pm)
  3. Monitor server process memory and WebSocket connection count via Grafana or equivalent
  4. Observe: memory climbs linearly from ~500MB to 4GB+, WebSocket connections accumulate far beyond the active user count
  5. Compare: same workload on v2.2 shows no memory growth and stable connection count

## Environment
Self-hosted TaskFlow v2.3, single server process on Ubuntu 22.04 VM with 8GB RAM, ~200 active users, real-time push notifications enabled

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout every workday, culminating in near-unusable response times by late afternoon. The team is forced to restart the server daily as a workaround, causing downtime.

## Recommended Fix
Investigate the WebSocket lifecycle management in the v2.3 real-time notification subsystem. Specifically look for: (1) connections not being closed on client disconnect/tab close/navigation, (2) reconnection logic that opens new connections without closing stale ones, (3) event listeners being attached per-connection without cleanup. Compare the connection handling code against v2.2 to identify what the 'improved real-time notifications' change introduced. Ensure connections are properly torn down and their resources freed when clients disconnect or reconnect.

## Proposed Test Case
Simulate a user session that repeatedly connects, disconnects, and reconnects WebSocket connections (mimicking page navigations, tab closes, and reconnects) over several hours. Assert that the total server-side WebSocket connection count never exceeds the number of concurrently active clients, and that server memory remains stable within a bounded range.

## Information Gaps
- The reporter's planned test of disabling real-time notifications to confirm the memory stays flat (would confirm but not change investigation direction)
- Exact WebSocket connection count at end of day vs. expected ~200 (reporter confirmed it's 'much higher' but didn't provide a specific number)
- Whether the v2.3 notification system uses a different WebSocket library or connection strategy than v2.2
