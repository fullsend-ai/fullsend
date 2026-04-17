# Triage Summary

**Title:** Intermittent 403 Forbidden for users with 'analyst' role — likely inconsistent role/permission config across load-balanced servers

## Problem
Users recently assigned the 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 requests to any dashboard page. Refreshing the page typically succeeds. The issue affects all analyst-role users and no other roles. No login redirect or session anomaly occurs on the successful retry.

## Root Cause Hypothesis
The application runs behind a load balancer with multiple backend servers. When the 'analyst' role was created or configured, the role-to-permission mapping was not consistently deployed or propagated to all servers. Requests routed to server(s) missing the analyst role's permissions return 403; requests routed to correctly configured server(s) succeed. This explains both the intermittency and the role-specific scope.

## Reproduction Steps
  1. Log in as a user assigned the 'analyst' role
  2. Navigate to the TaskFlow dashboard
  3. Refresh the page repeatedly (roughly 1 in 3 attempts should return 403)
  4. Confirm that users with 'editor' or 'admin' roles do not experience the error

## Environment
Multi-server deployment behind a load balancer. Exact server count and configuration unknown. The 'analyst' role was introduced/assigned a few days ago.

## Severity: high

## Impact
All users assigned the 'analyst' role are intermittently locked out of the entire dashboard. No other roles are affected. The only workaround is refreshing the page, which is unreliable and disruptive.

## Recommended Fix
1. Compare the role/permission configuration (RBAC tables, policy files, or authorization config) across all backend servers to identify which server(s) are missing the analyst role definition or its permission grants. 2. Deploy the correct analyst role permissions to all servers. 3. If permissions are cached, flush the authorization cache on all instances. 4. Review the deployment/config-sync process to ensure future role changes propagate atomically to all servers.

## Proposed Test Case
Send N sequential authenticated requests (e.g., 20) as an analyst-role user to the dashboard endpoint, pinning each request to a specific backend server (via direct IP or sticky header). Verify that every server returns 200. Repeat after any config deployment to confirm consistency.

## Information Gaps
- Exact number of backend servers and which specific instance(s) are misconfigured
- How the analyst role was provisioned — via database migration, config file, admin UI — and whether the deployment process covers all servers
- Whether the application caches role/permission lookups and what the cache TTL or invalidation mechanism is
