# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with new analyst role (permission caching issue)

## Problem
Users assigned to the recently created 'analyst' role are experiencing intermittent 403 Forbidden errors when loading the TaskFlow dashboard. The errors are non-deterministic — the same page load sometimes succeeds and sometimes returns 403. Refreshing the page one or more times eventually resolves it. The issue began approximately when the analyst role was created and assigned, and appears isolated to users with that role. Admin users have not reported the issue.

## Root Cause Hypothesis
The authorization/permission layer is caching role-permission mappings, and the cache is inconsistently populated for the new analyst role. On cache misses or stale cache hits, the system fails to recognize the analyst role's dashboard access grant and falls back to a deny (403). This explains the intermittent nature (cache hit vs. miss across requests or across multiple app servers) and why refreshing eventually works (a subsequent request hits a warm cache or a different server with the correct mapping).

## Reproduction Steps
  1. Create or use an account assigned only the 'analyst' role
  2. Navigate to the TaskFlow dashboard
  3. Refresh the page repeatedly (10-20 times) and observe that some loads return 403 while others succeed
  4. Compare with an admin-role account performing the same steps (expected: no 403s)

## Environment
Affects multiple users with the analyst role across the team; not browser- or network-specific. Role was created approximately 2 days before the report.

## Severity: high

## Impact
All users assigned the new analyst role are intermittently locked out of the dashboard, disrupting their workflow. As more users are assigned this role, the impact will grow.

## Recommended Fix
Investigate the permission/authorization caching layer: (1) Check whether the analyst role's permissions were correctly propagated to all cache nodes or app server instances. (2) Look for a race condition where the role-to-permission mapping is lazily loaded and occasionally evicted or missing. (3) Verify that role creation properly invalidates or warms the permission cache. (4) As an immediate mitigation, consider clearing the permission cache or restarting the authorization service to force a full reload of role mappings.

## Proposed Test Case
After fixing, write an integration test that creates a new role with dashboard access, assigns it to a test user, and verifies that 50+ consecutive dashboard requests all return 200 (no intermittent 403s). Additionally, test cache invalidation by creating a role, assigning permissions, and immediately verifying access without relying on cache warm-up.

## Information Gaps
- Exact cache technology and TTL configuration used by the authorization layer (internal architecture question)
- Whether the deployment uses multiple app servers or a load balancer that could explain request-level inconsistency (internal infrastructure question)
- Server-side error logs or authorization audit logs for the 403 responses (requires developer investigation)
