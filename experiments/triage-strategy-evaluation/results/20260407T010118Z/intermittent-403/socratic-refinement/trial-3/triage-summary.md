# Triage Summary

**Title:** Intermittent 403 on dashboard for users with newly created 'analyst' role (likely inconsistent permission propagation)

## Problem
Users assigned the newly created 'analyst' role (replacing 'viewer') receive 403 Forbidden errors on approximately 1 in 3 dashboard page loads. The failures are independent per request — refreshing can succeed, then fail again immediately. All users with the analyst role are affected. Users had no issues under the previous 'viewer' role.

## Root Cause Hypothesis
The new 'analyst' role's dashboard permission is not consistently recognized across all serving instances. This is most likely caused by one of: (1) multiple app server instances behind a load balancer where the role-to-permission mapping was not propagated to all nodes (e.g., stale permission cache, incomplete config deploy), (2) a permissions cache with inconsistent TTL or replication lag, or (3) an eventual-consistency issue in the authorization data store where the analyst role's grant is not yet replicated to all read replicas.

## Reproduction Steps
  1. Create or use a user account with only the 'analyst' role (no 'viewer' or other roles)
  2. Log in to TaskFlow
  3. Navigate to the dashboard
  4. Refresh the dashboard page 10–15 times in quick succession
  5. Observe that approximately 1 in 3 loads returns 403 Forbidden

## Environment
Affects all users with the newly created 'analyst' role. Multiple team members confirmed. Started approximately 2 days before report, coinciding with the introduction of the analyst role and role reassignment from 'viewer' to 'analyst'.

## Severity: high

## Impact
All users assigned the 'analyst' role are affected. Dashboard access is unreliable (~33% failure rate), blocking routine work. Likely affects the entire cohort of users migrated from 'viewer' to 'analyst'.

## Recommended Fix
1. Check whether the 'analyst' role has an explicit dashboard access permission granted — compare its permission set against the 'viewer' role it replaced. 2. If permissions are correctly configured, investigate whether the authorization layer uses caching or multiple instances — check for replication lag or nodes with stale role/permission mappings. 3. Inspect server-side access logs correlated with 403 responses to identify whether specific app server instances or permission cache states correlate with failures. 4. As an immediate mitigation, consider re-granting 'viewer' role alongside 'analyst' for affected users while the root cause is fixed.

## Proposed Test Case
Integration test: create a user with only the 'analyst' role, make 50 sequential authenticated requests to the dashboard endpoint, and assert that all 50 return 200. This validates consistent permission evaluation across requests regardless of which instance or cache state serves them.

## Information Gaps
- Whether the authorization system uses caching, replication, or multiple instances (server-side investigation needed)
- Exact permission entries configured for the 'analyst' role vs. the 'viewer' role
- Server-side logs showing which component returns the 403 (auth middleware, API gateway, application layer)
