# Triage Summary

**Title:** Memory leak in TaskFlow process since v2.3 upgrade — likely real-time notifications connection/listener leak

## Problem
The TaskFlow application process leaks memory throughout the day, climbing from ~500MB at startup to 4GB+ by end of day on a single-process deployment with 200 active users. This causes progressive slowdowns (10+ second page loads) and API timeouts, requiring daily restarts. The issue began exactly when upgrading from v2.2 to v2.3.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature is likely not cleaning up WebSocket connections, event listeners, or in-memory subscription state when users disconnect or navigate away. Memory growth correlates with active user count (steeper during business hours), consistent with per-connection or per-session objects accumulating without being garbage collected.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 as a single process (default configuration)
  2. Allow normal user activity from ~200 users over a business day
  3. Monitor the TaskFlow process memory via `top` or `docker stats`
  4. Observe memory climbing steadily, with a steeper slope during peak usage hours (9am-5pm)
  5. Memory will reach 3-4GB by end of day, causing page load times >10 seconds and API timeouts

## Environment
Ubuntu 22.04 VM, 8GB RAM, TaskFlow v2.3, single-process deployment (no workers), PostgreSQL and Redis running on same host but not contributing to the leak. ~200 active users, ~25 requests/user/hour at peak.

## Severity: high

## Impact
All 200 users experience progressively degraded performance throughout the day, with the application becoming effectively unusable by late afternoon. Operations team must restart the server daily. No data loss reported but availability is significantly impaired.

## Recommended Fix
Investigate the real-time notifications code path introduced in v2.3. Look for: (1) WebSocket or SSE connections not being closed/removed on client disconnect, (2) event listeners or pub/sub subscriptions accumulating without cleanup, (3) in-memory caches of notification state growing without eviction or TTL. Compare the connection lifecycle management with v2.2 to identify what changed. As an immediate mitigation, check if real-time notifications can be disabled via configuration flag to confirm the hypothesis and restore stable operation while a fix is developed.

## Proposed Test Case
Deploy TaskFlow v2.3 in a test environment, simulate 50+ concurrent users connecting and disconnecting repeatedly over several hours, and assert that memory usage remains bounded (e.g., stays below 1.5GB). Additionally, verify that after all users disconnect, memory returns to near-baseline levels within a reasonable GC window.

## Information Gaps
- Exact configuration flag (if any) to disable real-time notifications in v2.3
- Whether the leak is in WebSocket connections, SSE streams, or an in-memory data structure
- Whether v2.3.x patch releases have already addressed this
