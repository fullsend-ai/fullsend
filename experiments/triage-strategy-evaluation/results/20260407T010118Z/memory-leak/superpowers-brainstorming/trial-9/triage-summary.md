# Triage Summary

**Title:** Memory leak regression in v2.3: request-correlated memory growth requires daily restart

## Problem
After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory in proportion to user activity. Memory climbs from ~500MB at startup to 4GB+ over a business day with 200 active users, causing page loads of 10+ seconds and API timeouts by late afternoon. The server must be restarted daily. The leak is dormant during low-activity periods (weekends), confirming it is tied to request handling, not background processes.

## Root Cause Hypothesis
A code change in v2.3 introduced a per-request resource leak — most likely objects being added to an in-memory cache, event listener list, or connection pool that are never released. Common patterns: missing cleanup in middleware, unbounded memoization/cache without eviction, event listeners registered per-request but never removed, or database/HTTP connections not being returned to their pool.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with default configuration
  2. Start monitoring memory usage (e.g., process RSS or heap size)
  3. Simulate sustained user activity (200 concurrent users or equivalent load test)
  4. Observe memory climbing steadily over several hours without returning to baseline
  5. Compare with the same test against v2.2 to confirm the regression

## Environment
Self-hosted TaskFlow v2.3, ~200 active users, upgraded from v2.2 approximately one week ago. Grafana monitoring in place showing consistent day-over-day memory growth pattern.

## Severity: high

## Impact
All 200 users experience degraded performance (10+ second page loads, API timeouts) by late afternoon every day. Requires manual daily restart as a workaround, risking service disruption and data loss if the restart is missed and the server exhausts memory.

## Recommended Fix
1. Diff v2.2 and v2.3, focusing on request-handling code paths: middleware, controllers, caches, connection pools, and event listener registration. 2. Run a heap profiler (or equivalent for the server's runtime) under load against v2.3 to identify which object types are accumulating. 3. Look specifically for: unbounded caches missing eviction policies, event listeners attached per-request without cleanup, connection/resource handles not closed, and closures capturing large scopes in hot paths. 4. Once identified, fix the leak and verify memory stays stable under sustained load.

## Proposed Test Case
A soak test that simulates 200 concurrent users over a 4–8 hour period and asserts that server memory (RSS or heap) remains within a bounded range (e.g., does not exceed 2× the startup baseline). This test should run against both v2.2 (as a passing baseline) and the patched v2.3.

## Information Gaps
- Exact server runtime and framework (Node.js, Python, Java, etc.) — affects which profiling tools to use, but does not change the investigation approach
- Whether any v2.3-specific features (new integrations, plugins, config options) are enabled that might narrow the search — developer can check the changelog
