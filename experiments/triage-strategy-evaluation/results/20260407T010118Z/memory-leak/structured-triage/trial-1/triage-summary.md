# Triage Summary

**Title:** Memory leak in TaskFlow v2.3 causes progressive server slowdown requiring daily restart

## Problem
A self-hosted TaskFlow v2.3 instance experiences a steady memory leak, climbing from ~500MB at startup to 4GB+ over the course of a day. This causes progressive performance degradation (10+ second page loads, API timeouts) and requires a daily server restart. The issue affects approximately 200 active users.

## Root Cause Hypothesis
The real-time notification system introduced or reworked in v2.3 is likely leaking memory — possibly through accumulated WebSocket connections, event listeners, or notification state that is never garbage collected. The reporter noted significantly more verbose logging from this subsystem compared to v2.2, which aligns with the 'improved real-time notifications' feature in the v2.3 changelog.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server (Ubuntu 22.04, bare metal)
  2. Allow ~200 users to use the application throughout a normal workday
  3. Monitor memory usage over the course of the day
  4. Observe memory climbing steadily from ~500MB at startup to 4GB+ by end of day
  5. Observe page load times degrading to 10+ seconds and API timeouts as memory grows

## Environment
TaskFlow v2.3 (upgraded from v2.2), Ubuntu 22.04, 8GB RAM VM, bare metal deployment, ~200 active users

## Severity: high

## Impact
All ~200 users on the instance experience progressively degrading performance throughout each workday, with the application becoming effectively unusable by late afternoon. Requires manual daily restart as a workaround.

## Recommended Fix
Investigate the real-time notification system changes between v2.2 and v2.3. Look for: (1) WebSocket connections or event listeners that are opened but never closed/cleaned up, (2) in-memory notification state or caches that grow without bounds, (3) subscription handlers that accumulate per-user without deregistration. A heap snapshot comparison between startup and several hours of operation should pinpoint the leaking objects.

## Proposed Test Case
Run a load test simulating 200 users connecting and disconnecting over a simulated 8-hour period. Assert that memory usage remains within a bounded range (e.g., stays below 1.5x the startup baseline) and does not exhibit monotonic growth. Additionally, verify that WebSocket/notification listener counts do not grow unboundedly over time.

## Information Gaps
- Exact Node.js runtime version on the server
- Exact database type and version
- Whether the memory leak occurs with fewer users or under idle conditions (to distinguish per-request vs. connection-based leak)
