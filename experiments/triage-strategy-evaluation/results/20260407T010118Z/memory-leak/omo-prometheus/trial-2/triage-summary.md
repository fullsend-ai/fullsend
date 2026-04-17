# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications: WebSocket/connection leak causes unbounded memory growth proportional to user activity

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the main TaskFlow process exhibits a steady memory leak, climbing from ~500MB at startup to 3-4GB+ over an 8-hour business day. Page loads degrade to 10+ seconds and API calls time out, requiring a daily server restart. The leak rate correlates directly with active user count.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 is not properly closing or cleaning up connections (likely WebSocket or long-poll connections) when users disconnect or navigate away. Each user session accumulates orphaned connections that are never released, causing both memory growth and connection count to climb unboundedly. The Go process holds these leaked connections and their associated buffers in memory.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server (Ubuntu 22.04, 8GB RAM used by reporter)
  2. Allow ~200 users to use the application normally during business hours
  3. Monitor memory usage of the main TaskFlow process over several hours
  4. Observe memory climbing steadily from ~500MB toward 3-4GB over 8 hours
  5. Run `ss -s` or `netstat` against the TaskFlow port and observe established connections far exceeding the active user count
  6. Compare with v2.2 where the server runs for weeks without memory growth

## Environment
Self-hosted TaskFlow v2.3 on Ubuntu 22.04 VM, 8GB RAM, Go runtime, ~200 active users during business hours

## Severity: high

## Impact
All self-hosted TaskFlow v2.3 instances with active users are affected. The server becomes unusable (10+ second page loads, API timeouts) by end of business day and requires daily restarts. This is a regression from v2.2 which ran stably for weeks.

## Recommended Fix
Investigate the real-time notifications code path introduced or modified in v2.3. Specifically look for: (1) WebSocket or HTTP long-poll connection handlers that don't clean up on client disconnect, (2) missing `defer conn.Close()` or equivalent cleanup in Go connection handlers, (3) event listener or goroutine accumulation per connection without corresponding teardown, (4) connection registry or subscriber map that adds entries but never removes them on disconnect/timeout. Adding a connection idle timeout and proper disconnect handling should resolve the leak.

## Proposed Test Case
Write a load test that simulates 50+ users connecting and disconnecting repeatedly over a 1-hour period. Assert that: (1) the active connection count never exceeds the number of currently connected clients by more than a small margin, (2) memory usage remains stable (within 20% of baseline) after all clients disconnect, and (3) goroutine count returns to baseline after clients disconnect.

## Information Gaps
- Exact number of established connections from `ss` (reporter confirmed 'way more than 200' but exact count not yet provided)
- Whether TaskFlow has a configuration toggle to disable real-time notifications (reporter is checking)
- Specific log entries when grepping for WebSocket/notification-related warnings (reporter hasn't done targeted grep yet)
