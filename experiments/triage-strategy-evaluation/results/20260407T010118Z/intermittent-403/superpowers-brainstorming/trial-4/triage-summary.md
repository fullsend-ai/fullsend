# Triage Summary

**Title:** Intermittent 403 Forbidden errors for users with newly assigned 'analyst' role — likely role cache/propagation inconsistency across server instances

## Problem
Users who were recently assigned the 'analyst' role are experiencing intermittent 403 Forbidden errors when loading the TaskFlow dashboard. Approximately 1 in 3 page loads fail, but refreshing typically resolves the error. Multiple users with the same new role are affected. The issue began 2-3 days ago, coinciding with when the role was assigned.

## Root Cause Hypothesis
The 'analyst' role permissions are not consistently propagated or cached across all application server instances behind the load balancer. When a request hits an instance that has the updated role/permissions, the dashboard loads normally. When it hits an instance with stale role data, the user gets a 403 because that instance doesn't recognize the analyst role as authorized for the dashboard.

## Reproduction Steps
  1. Assign a user the 'analyst' role
  2. Have that user navigate to the TaskFlow dashboard
  3. Repeat page loads — approximately 1 in 3 should return a 403 Forbidden
  4. Refresh the page — the dashboard should load on a subsequent attempt

## Environment
Production environment, behind a load balancer with multiple server instances. Issue affects multiple users who recently received the 'analyst' role.

## Severity: high

## Impact
All users newly assigned the 'analyst' role are unable to reliably access the dashboard. The workaround (refreshing) is available but disruptive. This likely affects any new analyst role assignments going forward.

## Recommended Fix
Investigate the role/permission caching and propagation mechanism across server instances. Check: (1) whether the analyst role was recently added to the system and if all instances have the updated role definitions, (2) whether there is a permissions cache with a stale TTL or missing invalidation on role assignment, (3) whether a rolling restart or cache clear resolves the issue across all instances. Also verify the analyst role is correctly mapped to dashboard access in the authorization layer.

## Proposed Test Case
Assign the analyst role to a test user, then make 20+ sequential requests to the dashboard endpoint and verify all return 200. Additionally, add an integration test that assigns a new role and immediately verifies access across multiple simulated backend instances.

## Information Gaps
- Whether users without the analyst role (or with longer-standing roles) are also affected
- Number of server instances behind the load balancer
- Whether the analyst role is a newly created role vs. a pre-existing one
