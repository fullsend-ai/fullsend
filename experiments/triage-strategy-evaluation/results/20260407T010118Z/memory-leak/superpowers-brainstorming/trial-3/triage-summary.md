# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications: server-side resources never freed on disconnect

## Problem
After upgrading to TaskFlow v2.3, the server leaks memory at a rate proportional to active user count, climbing from ~500MB to 4GB+ over a business day. Memory is never reclaimed — not when users disconnect, log out, or go idle. Only a full server restart recovers it. This started immediately after the v2.3 upgrade, which introduced 'improved real-time notifications.'

## Root Cause Hypothesis
The v2.3 real-time notification system is allocating server-side resources per user session (likely WebSocket connection state, event listeners, or in-memory subscription/channel objects) that are never cleaned up when the session ends. The fact that memory doesn't drop at all on disconnect indicates missing or broken teardown logic in the connection lifecycle — the server is likely adding to a collection or registering listeners on connect but never removing them on disconnect.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server and monitor memory usage
  2. Have multiple users log in and use the application normally (real-time notifications must be active)
  3. Observe server memory climbing proportionally with number of user sessions
  4. Have all users log out or close their browsers
  5. Observe that server memory remains at its peak and does not decrease

## Environment
Self-hosted TaskFlow v2.3, ~200 active users, memory monitored via Grafana

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout the day, with page loads exceeding 10 seconds and API timeouts by late afternoon. Requires daily manual server restarts to remain usable.

## Recommended Fix
Examine the v2.3 diff for the real-time notifications feature. Look for: (1) WebSocket or SSE connection handlers that register listeners or add to collections without corresponding cleanup on disconnect/close events, (2) in-memory subscription maps or notification queues that grow per-session but are never pruned, (3) event emitter listeners added per connection without removeListener on teardown. Add proper cleanup in the connection close/disconnect handler and verify with a test that simulates connect-disconnect cycles.

## Proposed Test Case
Write an integration test that opens N WebSocket/notification connections, verifies they are tracked server-side, then closes all connections and asserts that the server-side resource count (subscriptions, listeners, connection objects) returns to zero. Additionally, monitor process memory over 100+ connect-disconnect cycles and assert it stays within a bounded range.

## Information Gaps
- Exact server-side technology stack (Node.js, Python, etc.) — but the leak pattern is framework-agnostic and the developer will know this
- Whether rolling back to pre-v2.3 resolves the issue — strongly implied by the timing but not confirmed
