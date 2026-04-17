# Triage Summary

**Title:** Memory leak in v2.3: notification/WebSocket event listeners accumulate during user activity

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage climbs steadily from ~500MB to 4GB+ during business hours, causing progressive slowdown (10+ second page loads, API timeouts) and requiring daily restarts. The issue did not exist on v2.2.

## Root Cause Hypothesis
The v2.3 upgrade introduced a leak in the notification subsystem — likely WebSocket connection handlers or event listeners that are registered per user activity but never cleaned up. The high volume of WebSocket connection messages and event listener registration log entries supports this. Each user interaction likely adds listeners that are not removed when connections close or users navigate away.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server (bare metal, Ubuntu 22.04)
  2. Allow ~200 users to use the system normally during business hours
  3. Monitor memory usage via Grafana or similar — memory will climb steadily
  4. After several hours, page loads degrade to 10+ seconds and API calls time out
  5. Restarting the server resets memory to ~500MB

## Environment
TaskFlow v2.3 (upgraded from v2.2), Ubuntu 22.04 VM with 8GB RAM, bare metal deployment, PostgreSQL (default install), ~200 active users

## Severity: high

## Impact
All 200 users experience progressively degraded performance throughout each business day. The team must restart the server daily, causing a maintenance burden and potential disruption.

## Recommended Fix
Diff the notification/WebSocket subsystem between v2.2 and v2.3 to identify changes. Look specifically for event listener registrations that lack corresponding cleanup (removeListener/off calls) on WebSocket disconnect or session end. A heap snapshot comparison between startup and after several hours of use would confirm the leaking objects. Check for patterns like registering listeners inside request handlers without removing them.

## Proposed Test Case
Simulate repeated WebSocket connections and disconnections (e.g., 100 connect/disconnect cycles). Assert that the count of registered event listeners and memory usage return to baseline after all connections close. This can be a load test or unit test against the notification module.

## Information Gaps
- Exact PostgreSQL and runtime (Node.js/Python) versions — unlikely to be the root cause given the v2.3 correlation
- Specific log snippets showing the WebSocket/event listener entries — would confirm the pattern but the description is sufficient for investigation
- Whether the v2.3 changelog mentions notification system changes
