# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with 'analyst' role

## Problem
Users assigned the 'analyst' role experience random 403 Forbidden errors when loading the TaskFlow dashboard. The error occurs on initial page load and subsequent navigations alike. Refreshing the page 1-2 times resolves it temporarily. The issue affects all users with the analyst role and no users with other roles (admin, editor). When the page does load, it renders with full expected access — no partial restrictions.

## Root Cause Hypothesis
The 'analyst' role was likely added recently and its permission mapping is inconsistent across the authorization layer. Most probable causes: (1) a permission/policy cache that intermittently serves stale data missing the analyst role's dashboard grant, (2) multiple application instances behind a load balancer where not all instances have the updated role definition (incomplete deployment rollout), or (3) a race condition in role resolution where the analyst role's permissions are loaded asynchronously and sometimes not available when the auth check runs.

## Reproduction Steps
  1. Assign a test user the 'analyst' role (and no other roles)
  2. Attempt to load the TaskFlow dashboard repeatedly (10-20 times)
  3. Observe that some requests return 403 Forbidden while others succeed
  4. Compare with a user who has the 'editor' or 'admin' role — those should never 403

## Environment
Affects multiple users across at least one team. Started approximately 2 days before report, coinciding with analyst role assignment and a possible deployment. Browser/OS not a factor given multiple affected users.

## Severity: high

## Impact
All users with the 'analyst' role are intermittently locked out of the dashboard, disrupting their workflow. The issue is organization-wide for that role, not isolated to one user.

## Recommended Fix
Investigate the authorization path for the 'analyst' role: (1) Check if the analyst role's dashboard permission grant exists consistently across all app instances or permission store replicas. (2) Inspect any permission/role caching layer for stale entries or TTL issues. (3) Review recent deployments around the time the analyst role was introduced for incomplete rollouts. (4) Check the role definition itself — ensure 'analyst' has an explicit dashboard access grant rather than relying on an implicit or inherited permission that may resolve inconsistently.

## Proposed Test Case
Create an integration test that assigns a user the 'analyst' role and makes 50 sequential authenticated requests to the dashboard endpoint, asserting that all return 200. Run this against a single instance and then behind the load balancer to isolate whether the inconsistency is intra-instance (caching) or inter-instance (deployment).

## Information Gaps
- Exact deployment history around the time the issue started (team can check CI/CD logs)
- Whether the analyst role was added via a migration, config change, or admin UI (team can check audit logs)
- Server-side authorization architecture details (caching layer, number of instances, role resolution mechanism)
