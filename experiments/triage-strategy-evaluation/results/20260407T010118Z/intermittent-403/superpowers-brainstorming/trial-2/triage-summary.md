# Triage Summary

**Title:** Intermittent 403 Forbidden errors for users with the new analyst role

## Problem
Users assigned the recently created 'analyst' role are experiencing random 403 Forbidden errors across all dashboard pages. The errors are intermittent — refreshing usually resolves them temporarily. Users with admin or editor roles are unaffected. The issue started approximately two days ago, coinciding with the introduction of the analyst role.

## Root Cause Hypothesis
The analyst role was added recently but its permissions are not being consistently evaluated across requests. Most likely causes: (1) a permissions cache that intermittently serves stale data missing the new role, (2) inconsistent role/permission configuration across multiple app server instances behind a load balancer, or (3) a race condition in permission lookup where the analyst role is not always resolved correctly.

## Reproduction Steps
  1. Log in as a user with the analyst role
  2. Navigate to the dashboard
  3. Refresh the page repeatedly — some loads will return 403, others will succeed
  4. Compare with an admin or editor account, which will not experience 403s

## Environment
Affects all users with the analyst role. Not environment-specific on the client side. Server-side configuration or caching is the likely factor.

## Severity: high

## Impact
All users with the analyst role are intermittently locked out of the dashboard, blocking their work. The role was presumably created to onboard a group of users who currently have an unreliable experience.

## Recommended Fix
1. Check the permission/authorization middleware for how the analyst role is resolved — look for caching layers (Redis, in-memory) that may not have been invalidated when the role was created. 2. If running multiple app instances, verify the role definition and permission grants are consistent across all instances. 3. Check for any role-permission mapping that requires a restart or cache flush to take effect. 4. Review recent deployments or configuration changes from ~2 days ago related to role management.

## Proposed Test Case
Create a user with the analyst role and make 50 sequential authenticated requests to the dashboard endpoint. Assert that all return 200. Additionally, add an integration test that creates a new role, assigns permissions, and verifies immediate consistent access without cache-dependent failures.

## Information Gaps
- Exact deployment topology (load balancer, number of instances) — but the dev team would know this
- Whether a permission caching layer exists and its TTL configuration
- Whether any deployment or config change was made at the same time as the role creation
