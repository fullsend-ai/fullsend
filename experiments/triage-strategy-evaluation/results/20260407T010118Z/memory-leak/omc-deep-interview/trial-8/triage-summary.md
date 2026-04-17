# Triage Summary

**Title:** Memory leak in v2.3 causes linear memory growth (~500MB to 4GB+/day), requiring daily restarts

## Problem
After upgrading from v2.2 to v2.3 approximately one week ago, the self-hosted TaskFlow instance exhibits a steady, linear memory leak that grows from ~500MB at startup to 4GB+ by end of day. This causes progressively degraded performance (10+ second page loads, API timeouts) and requires a nightly service restart. Memory remains pinned at the high-water mark even after user activity drops to zero, confirming it is a true leak rather than a load-scaling issue.

## Root Cause Hypothesis
The v2.3 release includes 'improved real-time notifications,' and the memory growth correlates closely with user activity volume (slower growth on weekends with ~20 users vs. weekdays with ~200). The most likely cause is that the revamped real-time notification or WebSocket subsystem in v2.3 is accumulating objects (e.g., event listeners, subscription state, message buffers, or connection metadata) per user interaction without releasing them. The leak is per-request or per-event rather than per-connection, given the linear growth pattern proportional to activity volume rather than connected user count.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with real-time notifications enabled
  2. Have a user base of ~200 active users generating ~25 API requests/hour each during peak
  3. Monitor memory usage via Grafana or equivalent over the course of a full workday
  4. Observe steady linear memory growth that does not recede when user activity drops
  5. Compare with v2.2 under the same conditions (no leak observed prior to upgrade)

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2 ~1 week ago). ~200 active users on weekdays, ~20 on weekends. Real-time notifications enabled, built-in Slack webhook integration active, no custom plugins. Database connection count is normal and stable.

## Severity: high

## Impact
All ~200 active users experience progressively degraded performance throughout each workday, with page loads exceeding 10 seconds and API timeouts by late afternoon. Operations team must restart the service nightly. This has been ongoing for approximately one week since the v2.3 upgrade.

## Recommended Fix
1. Diff the real-time notification subsystem between v2.2 and v2.3 — focus on WebSocket connection handling, event listener registration, subscription lifecycle, and any in-memory caches or buffers added in the 'improved' implementation. 2. Take a heap dump from a running v2.3 instance after several hours of use and analyze object retention — look for growing collections of notification events, subscriber entries, or connection metadata. 3. Check whether event listeners or subscriptions are properly cleaned up on WebSocket disconnect/reconnect. 4. The reporter is testing with real-time notifications disabled — if memory stabilizes, this confirms the notification subsystem as the source. 5. Consider whether a rollback to v2.2 is viable as a short-term mitigation.

## Proposed Test Case
Start a TaskFlow v2.3 instance with real-time notifications enabled. Simulate 100+ concurrent users making repeated API requests over a 2-hour period. Verify that memory usage returns to near-baseline within a reasonable time after all users disconnect and activity stops. Compare memory behavior with real-time notifications disabled under the same load profile. A passing test means memory is reclaimed; a failing test means memory remains pinned.

## Information Gaps
- Results of the reporter's planned test with real-time notifications disabled (in progress — reporter will report back)
- Heap dump or object-level memory profile identifying the specific leaking objects
- Exact WebSocket/long-lived connection count at high-memory state
- Whether the v2.3 upgrade also changed any server-side caching or session handling beyond notifications
