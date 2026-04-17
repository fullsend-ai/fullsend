# Triage Summary

**Title:** Memory leak regression in TaskFlow v2.3 causes progressive slowdown requiring daily restart

## Problem
After upgrading from TaskFlow v2.2 to v2.3, the server exhibits a memory leak where RSS climbs from ~500MB at startup to 4GB+ over the course of a workday. This causes page loads to degrade to 10+ seconds and API timeouts by late afternoon, requiring a daily restart. The issue affects all 200 active users on this self-hosted instance.

## Root Cause Hypothesis
A change introduced in v2.3 is leaking memory — likely an object cache, event listener, or connection pool that grows without bound. The v2.2→v2.3 changelog should reveal new or modified subsystems that allocate long-lived objects.

## Reproduction Steps
  1. Deploy TaskFlow v2.3 (self-hosted) with default configuration
  2. Allow ~200 users (or simulated load) to use the application over a full workday
  3. Monitor memory usage via process metrics or Grafana
  4. Observe memory climbing steadily from ~500MB toward 4GB+ over 8-10 hours
  5. Confirm the issue does NOT occur on v2.2 under identical conditions

## Environment
Self-hosted TaskFlow v2.3, ~200 active users, Grafana monitoring in place. Specific OS/runtime details not collected but not needed to begin investigation.

## Severity: high

## Impact
All 200 users on this instance experience degraded performance daily. The workaround (daily restart) causes downtime and is unsustainable.

## Recommended Fix
1. Diff v2.2 and v2.3 for changes to caches, connection pools, event listeners, or long-lived data structures. 2. Run v2.3 under load with heap snapshots taken at intervals (e.g., every 2 hours) and compare retained object graphs to identify the leaking allocation site. 3. Once identified, ensure the leaked objects are properly released, evicted, or bounded. 4. Verify the fix by running a soak test and confirming memory stabilizes.

## Proposed Test Case
Soak test: run TaskFlow v2.3 under simulated load of 200 concurrent users for 12+ hours. Assert that RSS memory stays below a defined ceiling (e.g., 1.5GB) and does not exhibit monotonic growth. This test should be added to the CI suite for release qualification.

## Information Gaps
- Exact server OS, runtime version, and deployment method (Docker, bare metal, etc.) — not needed to begin investigation but may matter for reproduction
- Whether any v2.3-specific features or plugins are enabled that could be disabled as a workaround
- Whether the Grafana dashboard shows any other correlated metrics (e.g., DB connection count, request rate) — could accelerate root cause identification
