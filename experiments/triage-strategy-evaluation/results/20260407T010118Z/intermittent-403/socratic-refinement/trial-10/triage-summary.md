# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with new analyst role — likely inconsistent role configuration across app instances

## Problem
Users assigned the recently-introduced 'analyst' role are intermittently receiving 403 Forbidden errors when accessing the TaskFlow dashboard. The errors occur regardless of navigation method (fresh login, refresh, internal navigation). Users with other roles (editor, admin) are unaffected. The issue began approximately two days ago, coinciding with the rollout of the analyst role.

## Root Cause Hypothesis
The application runs behind a load balancer with approximately three backend instances (inferred from the 'roughly every third attempt fails' pattern). When the analyst role was deployed, not all instances received the updated authorization/permissions configuration. Requests routed to the misconfigured instance(s) fail with 403 because they do not recognize the analyst role's dashboard access grant. Refreshing works when the load balancer routes the retry to a correctly-configured instance.

## Reproduction Steps
  1. Assign a user the 'analyst' role
  2. Attempt to load the TaskFlow dashboard
  3. Refresh the page repeatedly — approximately 1 in 3 attempts should return 403 Forbidden
  4. Compare with a user who has the 'editor' or 'admin' role — those users should never see 403s

## Environment
Production environment, multiple team members affected. Analyst role was rolled out approximately two days ago. Load-balanced deployment suspected (multiple app or auth server instances).

## Severity: high

## Impact
All users with the analyst role are intermittently locked out of the dashboard. This is a blocking issue for the newly-rolled-out role, affecting team productivity and undermining trust in the new role assignment.

## Recommended Fix
1. Check all application/auth server instances for consistent RBAC or permissions configuration — look for an instance missing the analyst role's dashboard access grant. 2. Verify the deployment mechanism for role/permission changes ensures all instances are updated atomically. 3. If using a permissions cache, check TTL and invalidation across nodes. 4. As an immediate mitigation, restart or redeploy all instances to force config synchronization.

## Proposed Test Case
Send N sequential dashboard requests (e.g., 30) authenticated as an analyst-role user and assert that all return 200. Run this against each backend instance individually (bypassing the load balancer) to confirm parity.

## Information Gaps
- Exact number of app/auth server instances behind the load balancer
- Whether the analyst role was newly created or modified from an existing role
- Deployment mechanism used for the role rollout (config change, DB migration, feature flag, etc.)
