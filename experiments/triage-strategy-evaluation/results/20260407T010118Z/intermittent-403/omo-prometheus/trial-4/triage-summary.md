# Triage Summary

**Title:** Intermittent 403 Forbidden errors for users with 'analyst' role — likely authorization caching or multi-instance permission inconsistency

## Problem
Users who were recently assigned the 'analyst' role are experiencing intermittent 403 Forbidden errors across all dashboard pages. Approximately one-third of page loads fail, but retrying (refreshing) typically succeeds within 2-3 attempts. Users with the 'admin' role are completely unaffected. The issue began approximately two days ago, coinciding with the analyst role being assigned to the affected users.

## Root Cause Hypothesis
The 'analyst' role's permissions are inconsistently resolved across requests, most likely due to one of: (1) Multiple application server instances behind a load balancer where one or more instances have stale or misconfigured permission/role mappings for the analyst role — the ~33% failure rate suggests roughly 1-in-3 backends may be affected. (2) An authorization caching layer (e.g., Redis, in-memory cache) with a race condition or partial invalidation issue introduced when the analyst role was created or modified. (3) The analyst role definition itself may reference permissions that intermittently fail to resolve (e.g., a wildcard or dynamic permission that depends on an unreliable lookup).

## Reproduction Steps
  1. Assign a user the 'analyst' role in TaskFlow
  2. Log in as that user and navigate to the dashboard
  3. Click through various pages (reports, tasks, any section)
  4. Observe that approximately 1 in 3 page loads returns a 403 Forbidden
  5. Refresh the page — it will typically load successfully on retry
  6. Repeat with an 'admin' role user to confirm they are NOT affected

## Environment
Production TaskFlow instance. Affects multiple users across at least one team. All affected users share the 'analyst' role assigned approximately 2 days ago. Admin-role users are unaffected. No other environment specifics available from reporter.

## Severity: high

## Impact
All users with the 'analyst' role are affected. Roughly one-third of page loads fail, degrading productivity significantly. Users can work around it by refreshing, but this causes frustration and lost time. If the analyst role is being rolled out to more users, impact will grow.

## Recommended Fix
1. Check whether the application runs behind a load balancer with multiple backend instances. If so, compare the analyst role's permission configuration across all instances — look for stale deployments or inconsistent permission caches. 2. Inspect the authorization caching layer: check cache TTLs, invalidation logic, and whether the analyst role's permissions were fully propagated when the role was created. 3. Review the analyst role definition for any permissions that depend on dynamic lookups or external services that could intermittently fail. 4. Check application logs filtered to 403 responses for analyst-role users — correlate with specific server instance IDs to confirm whether failures cluster on specific backends. 5. As a quick mitigation, verify that restarting app servers or flushing the auth cache resolves the issue.

## Proposed Test Case
Create an integration test that: (1) assigns the 'analyst' role to a test user, (2) makes 50+ sequential authenticated requests to various dashboard endpoints, and (3) asserts that zero requests return 403. Run this test against each backend instance individually (bypassing the load balancer) to identify inconsistent nodes. Additionally, add a regression test that verifies new role assignments are fully propagated across all authorization backends before returning success.

## Information Gaps
- Number of application server instances and load balancer configuration (reporter does not have backend visibility)
- Whether any deployments or infrastructure changes occurred around the same timeframe (reporter is not in the loop on deployments)
- Exact authorization mechanism used (RBAC framework, caching layer, session store)
- Server-side logs showing which instances serve the 403 responses
