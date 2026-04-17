# Triage Summary

**Title:** Memory leak in TaskFlow v2.3 likely caused by real-time notifications — server OOMs daily

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server exhibits a memory leak that causes memory to climb from ~500MB at startup to 4GB+ by end of day, resulting in 10+ second page loads and API timeouts. The server requires a daily restart to remain usable.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 is the most likely source of the leak. The memory growth correlates strongly with user activity (slower on weekends, stops when no users are active), and the team heavily uses real-time notifications. A likely mechanism is that notification connections (e.g., WebSocket or SSE listeners) or their associated state are not being properly cleaned up when users disconnect or navigate away.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a Node.js v18.17.0 server
  2. Allow normal usage by multiple users with real-time notifications enabled
  3. Monitor memory usage over several hours of active use
  4. Observe steady memory climb that correlates with user activity
  5. Compare against a v2.2 deployment under the same conditions to confirm regression

## Environment
Ubuntu 22.04 VM, 8GB RAM, Node.js v18.17.0, TaskFlow v2.3, ~200 active users, direct install (no Docker)

## Severity: high

## Impact
All 200 users on this self-hosted instance experience progressively degrading performance throughout the workday, with the server becoming effectively unusable by late afternoon. Requires daily manual restarts. Any self-hosted v2.3 deployment with active real-time notification usage is likely affected.

## Recommended Fix
Investigate the real-time notifications changes introduced in v2.3 (diff against v2.2). Focus on: (1) WebSocket/SSE connection lifecycle — are connections properly cleaned up on client disconnect? (2) Per-connection state or event listener accumulation — are handlers being removed? (3) Take a heap snapshot on a running instance to identify the retained object graph. A Node.js --inspect session with heap profiling would quickly pinpoint the leaking objects.

## Proposed Test Case
Create a load test that simulates many users connecting to real-time notifications, performing actions that generate notifications, and then disconnecting. Run for an extended period and assert that memory usage remains bounded (e.g., returns to near-baseline after connections close). Verify no growth in WebSocket/SSE connection count beyond active users.

## Information Gaps
- Server-side logs during the degradation period (may reveal connection errors or warnings)
- Whether rolling back to v2.2 resolves the issue (reporter has not tried this)
- Exact number of concurrent real-time notification connections at peak
