# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for analyst role users — likely load balancer / server config inconsistency

## Problem
Users with the analyst role receive 403 Forbidden errors on roughly every 3rd request to the dashboard. The errors began approximately 2 days ago, coinciding with when the analyst role was assigned to these users. The entire page fails (not partial). Admins are unaffected. At least 2 users are impacted. Refreshing once or twice reliably restores access.

## Root Cause Hypothesis
The application is served behind a load balancer distributing requests across approximately 3 backend servers. One of these servers has a stale, missing, or incorrectly configured permission/authorization entry for the analyst role. Requests routed to that server return 403; requests to the other servers succeed. This explains the roughly-every-third-request failure pattern, the immediate recovery on refresh, and the role-specific scope.

## Reproduction Steps
  1. Log in as a user with the analyst role
  2. Navigate to the dashboard
  3. Refresh the page repeatedly (5-10 times)
  4. Observe that approximately every 3rd request returns a 403 Forbidden error
  5. Confirm the issue does not occur for admin users

## Environment
Affects users with the analyst role. At least 2 users confirmed affected. Admin role users are not affected. Issue began approximately 2 days ago. No specific browser/OS dependency reported — pattern is consistent across at least 2 different users/machines.

## Severity: high

## Impact
All users with the analyst role are likely affected. The dashboard is intermittently inaccessible (~33% of requests fail). Users can work around it by refreshing, but this degrades the experience significantly and may cause data loss if the 403 hits during form submissions or other write operations.

## Recommended Fix
1. Check how many backend servers/instances are behind the load balancer and compare their authorization/permission configurations for the analyst role. 2. Look for a recent deployment or config change ~2 days ago that may have been applied to only some instances (rolling deploy that partially failed, missed config sync, stale cache). 3. Verify the analyst role exists and has dashboard access permissions in all server instances' auth configuration. 4. If using a permission cache, check TTL and invalidation — one server may have cached a pre-analyst-role permission state.

## Proposed Test Case
Send N sequential authenticated requests to the dashboard endpoint as an analyst-role user and assert that all return 200. Optionally pin requests to each backend server individually (via sticky sessions or direct addressing) to identify which server returns 403.

## Information Gaps
- Exact number and identity of backend servers behind the load balancer
- Whether a deployment or configuration change occurred ~2 days ago
- Whether other roles besides analyst and admin are affected
- Whether the 403 also affects other analyst-accessible pages or only the dashboard
