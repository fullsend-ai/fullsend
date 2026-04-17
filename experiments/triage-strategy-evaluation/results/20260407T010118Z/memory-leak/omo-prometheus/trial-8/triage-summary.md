# Triage Summary

**Title:** Memory leak in v2.3: per-request allocation in notification system not freed, causes linear memory growth

## Problem
The TaskFlow application server process leaks memory at a rate proportional to request volume. Memory grows from ~500MB at startup to 4GB+ over an 8-hour workday with ~200 active users (~5,000 requests/hour), causing page loads >10s and API timeouts by late afternoon. Requires daily server restarts. Started immediately after upgrading from v2.2 to v2.3.

## Root Cause Hypothesis
The v2.3 'improved real-time notifications' feature is allocating state (likely goroutines, channel subscriptions, or notification payload objects) on every inbound API request that is not released when the request completes. Given the Go runtime and linear growth pattern, the most probable causes are: (1) a goroutine spawned per-request for notification delivery that blocks or never exits, (2) notification event objects appended to an in-memory structure (slice/map) without eviction, or (3) per-request listener/callback registrations on a shared notification bus that are never deregistered.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with default configuration (no special feature flags needed)
  2. Connect ~200 concurrent users with notification features active (default)
  3. Generate sustained API traffic (~25 requests/user/hour is the production pattern)
  4. Monitor process RSS via htop or Prometheus process_resident_memory_bytes
  5. Observe linear memory growth; compare to v2.2 under identical load where memory remains stable

## Environment
Self-hosted TaskFlow v2.3 (Go binary), PostgreSQL database (stable at ~400MB), ~200 active weekday users, Prometheus metrics and Grafana monitoring in place, previously stable on v2.2 for weeks without restarts

## Severity: high

## Impact
All ~200 users experience progressive performance degradation daily, with the application becoming effectively unusable (10s+ page loads, API timeouts) by late afternoon. Requires manual daily restart as a workaround. No data loss but significant productivity impact.

## Recommended Fix
1. Diff the notification system code between v2.2 and v2.3 — focus on any new per-request code paths (middleware, event emission, subscriber registration). 2. Run a load test against v2.3 with Go pprof heap profiling enabled (`/debug/pprof/heap`) and check `go_goroutines` metric for goroutine leaks. 3. Look specifically for: goroutines spawned per-request that block on channel reads, notification payloads accumulated in unbounded slices/maps, or event listener registrations that lack corresponding deregistration on request completion. 4. Verify the fix by confirming stable memory under sustained load over multiple hours, and compare against v2.2 baseline.

## Proposed Test Case
Load test that simulates 200 concurrent users making 25 requests/hour each over a simulated 8-hour period against v2.3. Assert that process RSS stays below a threshold (e.g., 1GB) and that goroutine count remains proportional to active connections rather than cumulative request count. Run the same test against v2.2 as a baseline to confirm the regression.

## Information Gaps
- Exact Go object types consuming memory (obtainable via pprof heap profile on a running instance)
- Whether goroutine count is also climbing (check go_goroutines Prometheus metric in existing Grafana)
- Which specific code change in v2.3 introduced the leak (requires v2.2↔v2.3 diff review)
- Whether disabling notifications (if configurable) stops the leak — would confirm the component but reporter didn't indicate this is configurable
