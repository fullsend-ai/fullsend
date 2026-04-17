# Triage Summary

**Title:** Intermittent 403 Forbidden for users with 'analyst' role — likely deployment inconsistency across backend instances

## Problem
Users assigned the newly-added 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 page loads across the entire application. The errors are not tied to specific pages, login state, or session age. Refreshing typically succeeds on the 2nd or 3rd attempt. Users with the 'admin' role are unaffected. The issue began roughly when the 'analyst' role was introduced.

## Root Cause Hypothesis
A load balancer is distributing requests across multiple backend instances (likely 3, given the ~1/3 failure rate), and one instance does not have the 'analyst' role properly registered in its authorization/permission configuration. This could be caused by a rolling deployment that didn't fully propagate, a stale config/cache on one instance, or the role definition existing in a config file or database cache that one instance hasn't picked up. When requests hit the misconfigured instance, it doesn't recognize the 'analyst' role and returns 403.

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the dashboard or any page
  3. Refresh or navigate repeatedly — approximately 1 in 3 requests will return a 403 Forbidden
  4. Note: the 403 is a standard server error page, not a custom TaskFlow error page

## Environment
Production environment. Multiple analyst-role users affected across the same team. Admin-role users are not affected. Issue started approximately 2 days ago, coinciding with the introduction of the 'analyst' role.

## Severity: high

## Impact
All users with the 'analyst' role are affected. The application is partially unusable — users can work around it by refreshing, but ~33% of requests fail, significantly degrading productivity and trust. No data loss, but core functionality is unreliable.

## Recommended Fix
1. Check how many backend instances are running and compare their authorization/role configurations — look for one that is missing the 'analyst' role definition. 2. Check if a recent deployment rolled out unevenly or if one instance is running an older version. 3. Verify the role config source (database, config file, env var) is consistent across all instances. 4. Restart or redeploy the misconfigured instance. 5. If roles are cached, check cache invalidation — one instance may have a stale cache from before the 'analyst' role was added.

## Proposed Test Case
Send 20+ sequential authenticated requests as an analyst-role user to the dashboard endpoint and assert all return 200. Optionally, pin requests to each backend instance individually (via direct IP or header) and verify each instance returns 200 for analyst-role users.

## Information Gaps
- Exact number of backend instances behind the load balancer
- Whether the 'analyst' role was added via config file, database migration, or environment variable
- Exact deployment timeline and whether a rolling deployment was used
- Server-side access logs correlating 403s to specific instance IPs
