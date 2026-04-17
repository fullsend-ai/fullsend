# Triage Summary

**Title:** Memory leak in v2.3 real-time notification system causes linear memory growth (~3.5GB/day) requiring daily restarts

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server exhibits a consistent memory leak. Memory climbs linearly from ~500MB at startup to 4GB+ over a workday under normal load (200 users, ~5000 req/hr), causing progressive slowdown with 10+ second page loads and API timeouts by late afternoon. The server requires daily restarts to remain functional.

## Root Cause Hypothesis
The 'improved real-time notifications' feature introduced in v2.3 is likely accumulating per-request state on the server side without proper cleanup. Since WebSocket connection count remains stable while memory grows linearly with request volume, the leak is not in connection handling but in request-scoped processing — most likely notification event objects, subscription listener registrations, or in-memory notification history/queues that are appended to on each API request but never released or bounded.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with default configuration
  2. Connect ~200 concurrent users (or simulate equivalent load of ~5000 API requests/hour) with persistent browser sessions using real-time notifications
  3. Monitor server memory usage over several hours
  4. Observe linear memory growth of approximately 400-500MB per hour under sustained load

## Environment
Self-hosted TaskFlow v2.3 (upgraded from v2.2 via standard upgrade path with database migration), ~200 active users, Slack and email notification integrations enabled, users maintain persistent browser sessions throughout the workday

## Severity: high

## Impact
All 200 users on this instance experience progressively degrading performance throughout the workday, culminating in effective unavailability by late afternoon. Requires daily manual restarts as a workaround, causing downtime and operational burden.

## Recommended Fix
Diff the real-time notification subsystem between v2.2 and v2.3, focusing on server-side request handling paths. Look for: (1) event listeners or callbacks registered per-request that are never removed, (2) in-memory collections (notification queues, event histories, subscriber lists) that grow without bounds or eviction, (3) closures capturing request-scoped objects that prevent garbage collection. Take a heap dump from a running instance after several hours of use to identify the dominant object types consuming memory. As a quick diagnostic, check whether disabling real-time notifications halts the memory growth.

## Proposed Test Case
Run a load test simulating 200 concurrent users with persistent WebSocket connections issuing ~25 API requests/user/hour for 8 hours. Assert that server memory usage remains within a bounded range (e.g., does not exceed 2x the baseline after startup stabilization). This test should be run against both v2.2 (as a control) and v2.3 to confirm the regression.

## Information Gaps
- Exact v2.3 changelog details beyond 'improved real-time notifications'
- Server-side heap profile or dominant object types in the leaked memory
- Whether the leak occurs with real-time notifications disabled
- Server runtime environment (Node.js version, container memory limits, etc.)
