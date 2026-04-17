# Triage Summary

**Title:** Intermittent 403 errors on dashboard caused by stale permissions cache on app-server-3 after role migration to 'analyst'

## Problem
Users who were migrated from the 'viewer' role to the 'analyst' role are receiving intermittent 403 Forbidden errors when loading the dashboard. Approximately 1 in 3 page loads fail. The issue affects multiple users who share the analyst role and began shortly after the role change was applied.

## Root Cause Hypothesis
When user roles were updated from 'viewer' to 'analyst', not all backend application servers invalidated or refreshed their permissions cache. Specifically, app-server-3 appears to be serving stale authorization data (either a cached role mapping or a cached authorization decision), causing it to reject requests from users whose roles were updated. App-server-1 has the correct permissions and serves requests successfully. The load balancer distributes requests across servers via round-robin, producing the intermittent pattern.

## Reproduction Steps
  1. Log in as a user whose role was recently changed from 'viewer' to 'analyst'
  2. Navigate to the TaskFlow dashboard
  3. Observe that ~1 in 3 page loads return a 403 Forbidden error
  4. On a 403 response, check the X-Served-By response header — it will show app-server-3
  5. Refresh the page until it succeeds — the X-Served-By header will show a different server (e.g., app-server-1)

## Environment
Production environment. Multiple backend application servers behind a load balancer (at least app-server-1 and app-server-3 identified). Users affected have the 'analyst' role that was recently assigned to replace the 'viewer' role.

## Severity: high

## Impact
All users with the 'analyst' role are affected. ~1 in 3 dashboard page loads fail with 403, disrupting normal workflow. Multiple team members confirmed affected. Workaround exists (refresh the page) but is disruptive.

## Recommended Fix
1. Investigate the permissions/role cache on app-server-3 — compare its cached role data with app-server-1's. 2. Force a cache invalidation or restart on app-server-3 as an immediate fix. 3. Audit all backend servers (not just these two) for the same stale cache issue. 4. Investigate the role-update pipeline: when a role change is written to the database, how are individual app servers notified to invalidate their caches? Fix the cache invalidation mechanism so future role changes propagate reliably to all servers (e.g., pub/sub cache invalidation, shorter TTLs, or event-driven cache busting).

## Proposed Test Case
After applying the fix, send 50 sequential dashboard requests for an analyst-role user and verify all return 200. Additionally, change a test user's role and confirm that all backend servers reflect the new role within the expected propagation window (verify via the X-Served-By header on each server).

## Information Gaps
- Total number of backend servers in the pool (only app-server-1 and app-server-3 observed so far)
- Whether app-server-2 or other servers also have stale caches
- The specific caching mechanism used for permissions (in-memory, Redis, etc.) and its TTL configuration
- Whether any recent deployments were rolled out unevenly across servers
