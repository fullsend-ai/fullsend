# Triage Summary

**Title:** Memory leak in v2.3 real-time notification system: event listeners registered per API request but never cleaned up

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow web server process leaks memory steadily throughout the day, growing from ~500MB at startup to 4GB+ by end of day, causing 10+ second page loads and API timeouts. The server requires a daily restart. This affects all 200 active users on the self-hosted instance.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature registers a new event listener on the WebSocket notification system for every incoming API request (page load, API call, etc.), but never removes or deregisters these listeners. While the WebSocket connections themselves are managed correctly (~210 connections for 200 users), the per-request listeners accumulate unboundedly in memory throughout the day. This is a classic event-listener leak — the connection is reused but listeners pile up on it.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server (Ubuntu 22.04, 8GB RAM used in report)
  2. Allow ~200 users to use the application normally throughout a workday
  3. Monitor memory usage of the main TaskFlow web server process over several hours
  4. Observe memory climbing steadily from ~500MB toward 4GB+
  5. Check TaskFlow logs for accumulating event listener registration messages with no corresponding cleanup/removal messages
  6. Verify WebSocket connection count remains normal (~1 per active user), confirming the leak is in listeners, not connections

## Environment
TaskFlow v2.3, Ubuntu 22.04 VM, 8GB RAM, ~200 active users, self-hosted. Issue does not occur on v2.2.

## Severity: high

## Impact
All users on any v2.3 instance are affected. Performance degrades progressively throughout the day, becoming unusable (10+ second loads, API timeouts) by late afternoon. Workaround exists (daily server restart) but is disruptive and unsustainable.

## Recommended Fix
Examine the v2.3 notification system code (likely introduced or modified in the 'improved real-time notifications' feature). Look for where event listeners are registered in the API request handling path — there should be a listener registration that fires on each incoming request to push real-time updates. Add proper lifecycle management: either (1) check for an existing listener before registering a new one (deduplicate per session/connection), (2) remove listeners when the request completes, or (3) attach listeners at WebSocket connection time rather than per-request. Diffing v2.2 and v2.3 notification system code should pinpoint the exact change that introduced the per-request registration.

## Proposed Test Case
Write a test that simulates a single WebSocket connection making N API requests (e.g., 1000) and asserts that the number of registered event listeners on that connection remains bounded (e.g., O(1) rather than O(N)). Additionally, monitor process memory after a sustained load test to verify it remains stable rather than growing linearly with request count.

## Information Gaps
- Exact log line format for the listener registration messages (would help pinpoint the code path but developer can grep for it)
- Whether a configuration flag exists to disable the real-time notification feature (could serve as an interim workaround beyond daily restarts)
