# Triage Summary

**Title:** Memory leak in v2.3 real-time notifications causes progressive server slowdown

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the self-hosted server exhibits a steady memory leak — climbing from ~500MB at startup to 4GB+ over the course of a workday. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, requiring a daily server restart. The issue did not exist on v2.2.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature is likely leaking memory — most probably through WebSocket connections or event listeners that are not being properly cleaned up when users disconnect or between notification dispatches. The linear, usage-correlated growth pattern (faster with 200 users, slower with 20) suggests per-connection or per-event resource accumulation.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 on a server with ~8GB RAM
  2. Allow normal user activity (~200 active users with real-time notifications enabled)
  3. Monitor memory usage over several hours via Grafana or similar
  4. Observe steady linear memory climb from ~500MB toward 4GB+
  5. Confirm page load times degrade to 10+ seconds and API begins timing out

## Environment
Ubuntu 22.04, 8GB RAM VM (~4 CPU cores), TaskFlow v2.3, ~200 active users, self-hosted deployment

## Severity: high

## Impact
All ~200 users on the instance experience progressively degrading performance throughout each workday, with the application becoming effectively unusable by late afternoon. Requires daily manual server restarts to maintain service.

## Recommended Fix
Investigate the real-time notifications changes introduced in v2.3. Focus on: (1) WebSocket connection lifecycle — are connections and associated event listeners properly cleaned up on disconnect? (2) In-memory subscriber/listener registries — are entries removed when no longer needed? (3) Notification dispatch buffers — are processed notifications being released? A heap snapshot comparison between v2.2 and v2.3 under load would quickly identify the leaking objects.

## Proposed Test Case
Create a load test that simulates repeated WebSocket connect/disconnect cycles and sustained notification delivery over several hours. Assert that memory usage remains within a bounded range (e.g., no more than 20% growth over baseline after reaching steady state) and that no WebSocket handlers or event listeners accumulate beyond the active connection count.

## Information Gaps
- Exact Node.js runtime version and database backend/version (unlikely to be relevant given the issue is version-regression specific)
- Server-side error logs or stack traces during the degraded state
- Heap dump or memory profiler output identifying specific leaking objects
