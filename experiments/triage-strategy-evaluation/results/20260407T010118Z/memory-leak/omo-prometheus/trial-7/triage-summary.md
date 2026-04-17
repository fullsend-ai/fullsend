# Triage Summary

**Title:** Memory leak in v2.3: goroutines accumulate per-request in real-time notification subsystem, never cleaned up

## Problem
The TaskFlow application server leaks memory from ~500MB to 4GB+ over a working day, causing 10+ second page loads and API timeouts by late afternoon. This requires a daily server restart and affects all 200 users. The issue began immediately after upgrading from v2.2 to v2.3.

## Root Cause Hypothesis
The real-time notifications feature introduced in v2.3 spawns a goroutine (or registers a callback/listener) on each incoming request that is never torn down. This causes goroutine count and memory to grow linearly with request volume throughout the day. The stable established connection count (~180-210) rules out a connection leak — the leak is internal to the request-handling path, likely a goroutine per notification dispatch or subscription registration that outlives the request lifecycle.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 with real-time notifications enabled (default)
  2. Drive normal user traffic (~200 active users or simulate equivalent load)
  3. Monitor goroutine count and memory via Grafana or Go pprof over several hours
  4. Observe goroutine count climbing linearly with no plateau, and memory growing from ~500MB toward 4GB+
  5. Compare with TaskFlow v2.2 under the same load — no leak observed

## Environment
Self-hosted TaskFlow v2.3, Go runtime (version bundled with v2.3 release), ~200 active users, previously stable on v2.2

## Severity: high

## Impact
All 200 users experience progressively degrading performance throughout each day, with the application becoming effectively unusable by late afternoon. Requires daily manual restart as a workaround.

## Recommended Fix
Examine the real-time notification code path introduced in v2.3 for goroutines spawned per-request that are not cancelled or cleaned up when the request completes. Likely candidates: (1) a goroutine spawned to push notification updates per request that blocks on a channel forever, (2) a listener/subscriber registered in a notification hub that is never deregistered, or (3) a context-derived goroutine where the context is never cancelled. A Go pprof goroutine dump (`/debug/pprof/goroutine?debug=1`) on a running instance late in the day will immediately reveal which function is accumulating goroutines. As a quick validation, disabling the real-time notification feature (if a feature flag exists) should stop the leak entirely.

## Proposed Test Case
Write a load test that simulates a day of typical traffic (e.g., 1000+ requests over a compressed timeframe) and asserts that goroutine count returns to baseline after requests complete. Goroutine count at the end of the test should be within a small delta of the count at the start — any sustained linear growth indicates the leak is still present.

## Information Gaps
- Exact Go pprof goroutine dump showing which function is accumulating (would confirm the specific code path but developer can obtain this themselves)
- Whether a feature flag exists to disable real-time notifications for quick validation
- Exact Go runtime version shipped with TaskFlow v2.3
