# Triage Summary

**Title:** Memory leak in v2.3 real-time notification system causes progressive server degradation

## Problem
The TaskFlow application process leaks memory proportional to request/event volume when the real-time notification feature (introduced in v2.3) is enabled. Memory grows from ~500MB at startup to 4GB+ over a business day with ~200 active users (~5,000 requests/hour), causing page loads >10s and API timeouts. Requires daily restart.

## Root Cause Hypothesis
The v2.3 real-time notification subsystem accumulates data per request or per notification event in an unbounded in-memory structure (e.g., notification history buffer, event replay queue, or listener/callback registry) that is never pruned or garbage collected. WebSocket/SSE connections themselves are managed correctly (stable count ~180-200), so the leak is in the per-event processing path, not connection lifecycle.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with ENABLE_REALTIME_NOTIFICATIONS=true
  2. Have multiple users (200 in reporter's case, but likely reproducible at smaller scale with proportionally longer observation) actively use the application, generating task updates and API requests
  3. Monitor the TaskFlow application process memory over several hours
  4. Observe memory climbing proportionally to request volume without recovery
  5. Disable ENABLE_REALTIME_NOTIFICATIONS and observe memory stabilizes immediately

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2), PostgreSQL database, Redis, 8GB VM, ~200 active users, ~25 API requests/user/hour during business hours

## Severity: high

## Impact
All users on self-hosted v2.3 instances with real-time notifications enabled experience progressive degradation throughout the day, eventually rendering the application unusable. Workaround exists (disable notifications via ENABLE_REALTIME_NOTIFICATIONS=false), but sacrifices a key v2.3 feature.

## Recommended Fix
Investigate the v2.3 real-time notification code path for unbounded in-memory data structures. Likely candidates: (1) an in-memory notification event log or replay buffer that appends per event but never evicts, (2) per-request callback/listener registrations that accumulate without cleanup, (3) a notification fan-out structure that caches events for delivery but never expires them. Look at what changed between v2.2 and v2.3 in the notification module. Add bounds (max size, TTL-based eviction, or periodic pruning) to whatever structure is growing.

## Proposed Test Case
Create a load test that simulates sustained API request volume (e.g., 1,000 requests/minute) with real-time notifications enabled. Monitor application process memory (RSS) over a 1-hour period. Assert that memory usage stabilizes (reaches a steady state) rather than growing linearly with cumulative request count. Compare against the same test with notifications disabled as a control.

## Information Gaps
- Exact internal data structure causing the accumulation (requires code inspection of v2.3 notification module)
- Whether a fix exists in a newer TaskFlow version (reporter asked but this is outside triage scope)
- Minimum user/request threshold to trigger noticeable growth
