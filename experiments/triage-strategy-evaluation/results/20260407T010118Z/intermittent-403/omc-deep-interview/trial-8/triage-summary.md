# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with 'analyst' role since recent deployment

## Problem
Users with the recently-introduced 'analyst' role receive 403 Forbidden errors approximately one-third of the time when accessing the main dashboard. Refreshing the page resolves the error without requiring re-authentication. Users with other roles (e.g., admin) are unaffected. The issue began coinciding with a deployment that introduced the analyst role.

## Root Cause Hypothesis
The recent deployment that added the 'analyst' role was not applied consistently across all backend instances behind the load balancer. Some instances recognize the analyst role's dashboard permissions and some do not (or have stale permission/RBAC configuration). Requests routed to updated instances succeed; requests routed to un-updated instances return 403. This explains both the role correlation and the intermittent, refresh-fixable pattern.

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the main dashboard page
  3. If the page loads successfully, refresh or revisit repeatedly (expect ~1 in 3 attempts to fail)
  4. Observe 403 Forbidden error on failed attempts
  5. Refresh the page — it should load on retry
  6. Compare behavior with a user who has a different role (e.g., admin) — they should never see the 403

## Environment
Production TaskFlow instance, post-deployment that introduced the 'analyst' role (approximately 2 days before report). Multiple team members affected. Likely load-balanced backend infrastructure.

## Severity: high

## Impact
All users with the 'analyst' role are affected. Dashboard access is unreliable (~33% failure rate), degrading usability. Workaround exists (refresh the page), preventing this from being critical, but the issue erodes trust and productivity for the analyst user population.

## Recommended Fix
1. Check whether the deployment that introduced the analyst role was fully rolled out across all backend instances/pods. Look for instances running a stale version or with stale RBAC/permission caches. 2. Verify the analyst role's permission mapping includes dashboard access in the authorization configuration. 3. If using permission caching, check whether stale caches on some instances are missing the new role. 4. Restart or redeploy any lagging instances and confirm the role-permission mapping is consistent across all nodes.

## Proposed Test Case
Automated test: authenticate as an analyst-role user and request the dashboard endpoint N times (e.g., 20) in succession. Assert that every request returns 200. Run this against each backend instance individually (bypassing the load balancer) to verify consistency.

## Information Gaps
- Exact deployment details and whether a rolling deploy completed successfully across all instances
- Backend architecture specifics (number of instances, load balancer configuration, permission caching strategy)
- Whether server-side logs show which instance serves the 403 responses
