# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications: WebSocket connections/listeners accumulate per request, never cleaned up

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory throughout the day, climbing from ~500MB at startup to 4GB+ by end of day, causing page loads of 10+ seconds and API timeouts. The server requires a daily restart. The issue affects all ~200 active users on this self-hosted instance.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 registers a WebSocket connection, event listener, or subscription on every API request rather than once per user session, and never tears them down. This causes linear accumulation proportional to total request volume (~200 users × ~25 requests/user/hour), explaining both the connection count (4,000-5,000 by end of day) and the memory growth.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 (upgrade from v2.2 or fresh install)
  2. Run with a meaningful number of concurrent users (reported with ~200)
  3. Monitor memory usage and WebSocket connection count over several hours
  4. Observe memory climbing linearly and WebSocket connections accumulating far beyond the active user count
  5. Compare to v2.2 where memory remains stable at ~500-600MB indefinitely

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2), ~200 active users, ~25 requests per user per hour. Monitoring via Grafana. Exact OS/runtime not specified but issue is in application-level connection management, not infrastructure.

## Severity: high

## Impact
All ~200 users on the instance experience progressively degrading performance throughout the day, culminating in 10+ second page loads and API timeouts by late afternoon. Requires daily manual restart as a workaround. No data loss reported, but productivity impact is significant.

## Recommended Fix
Investigate the real-time notifications code path introduced in v2.3. Look for WebSocket connection or event listener/subscription registration that occurs per-request rather than per-session. The fix likely involves: (1) ensuring notification subscriptions are created once per user session and reused, not per API request; (2) adding proper cleanup/teardown when sessions end or connections close; (3) adding a configuration toggle to disable real-time notifications as a mitigation option. Regression test against v2.2 behavior where memory stayed stable.

## Proposed Test Case
Create an automated test that simulates a single user session making 100+ API requests, then verifies that the number of active WebSocket connections/event listeners remains constant (1 per session) rather than growing with request count. Additionally, a soak test with multiple simulated users over an extended period should confirm memory remains bounded.

## Information Gaps
- Exact server runtime environment (Node.js version, OS, container vs bare metal) — useful for reproducing but not blocking
- Whether rolling back to v2.2 fully resolves the issue (strongly expected based on timeline but not explicitly tested)
- Exact nature of the leaked resource (WebSocket connection vs event emitter listener vs pub/sub subscription) — developer will identify from v2.3 diff
