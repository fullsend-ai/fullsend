# Triage Summary

**Title:** Memory leak in TaskFlow v2.3: notification handlers appear to accumulate, causing progressive slowdown

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server's memory usage climbs steadily from ~500MB to 4GB+ over the course of a day, causing page loads to exceed 10 seconds and API timeouts. The server requires a daily restart to recover. The issue did not occur on v2.2.

## Root Cause Hypothesis
The v2.3 release likely introduced a bug in the notification system where push notification handlers are registered repeatedly without being cleaned up. The growing volume of notification-related log entries throughout the day supports this — event listeners or handlers are accumulating and consuming memory.

## Reproduction Steps
  1. Install or upgrade to TaskFlow v2.3 on a server with ~8GB RAM
  2. Connect a PostgreSQL database and configure normally per official docs
  3. Allow ~200 users to use the system through a normal workday
  4. Monitor memory usage over several hours — expect steady climb from ~500MB
  5. Observe increasing notification handler log entries via journalctl -u taskflow
  6. By end of day, memory reaches 4GB+ and response times degrade to 10+ seconds

## Environment
Ubuntu 22.04, 8GB RAM VM, TaskFlow v2.3 (upgraded from v2.2), PostgreSQL on separate host, no reverse proxy, ~200 active users

## Severity: high

## Impact
All 200 users on this instance experience progressively degrading performance daily, with the application becoming effectively unusable by late afternoon. Requires daily manual restart as a workaround.

## Recommended Fix
Diff the notification system code between v2.2 and v2.3. Look for event listener or handler registrations that run on each request or on a recurring basis without corresponding cleanup/deregistration. Likely candidates: a subscribe/addEventListener call that was moved into a per-request code path, or a removed cleanup/dispose call. Add proper handler deregistration or deduplicate handler registration so each handler is registered only once.

## Proposed Test Case
Write a test that simulates repeated notification registration cycles (e.g., 1000 iterations of whatever triggers handler setup) and asserts that the number of registered handlers and memory usage remain bounded. Additionally, a soak/load test running for several simulated hours with concurrent users should show stable memory.

## Information Gaps
- Exact Node.js/runtime version (reporter was unsure; docs-based install likely uses a known version)
- Whether the issue reproduces with fewer users or requires the full 200-user load
- Specific v2.3 changelog entries related to the notification system
