# Triage Summary

**Title:** Intermittent 403 Forbidden for users with 'analyst' role — likely inconsistent role config across load-balanced instances

## Problem
Users assigned the newly introduced 'analyst' role receive 403 Forbidden errors on approximately 1 out of every 3 requests to the dashboard. The entire page fails (not partial). Users with other roles (e.g., 'editor') are unaffected. The issue began when the 'analyst' role was deployed ~2 days ago. Re-authentication does not resolve it.

## Root Cause Hypothesis
The new 'analyst' role permissions were not consistently applied across all backend/application server instances behind the load balancer. One (or more) instances is missing the role definition or its associated permission grants. Requests routed to the misconfigured instance(s) return 403; requests to correctly configured instances succeed. The ~33% failure rate suggests 1 of 3 instances is affected.

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the TaskFlow dashboard
  3. Refresh the page 6+ times in quick succession
  4. Observe that approximately 1 in 3 requests returns a 403 Forbidden error
  5. Repeat with a user who has the 'editor' role — no 403s should occur

## Environment
Production TaskFlow deployment, load-balanced across multiple backend instances. New 'analyst' role was deployed approximately 2 days ago. Exact infrastructure details (number of instances, load balancer type) need to be confirmed with the ops team.

## Severity: high

## Impact
All users assigned the 'analyst' role are intermittently locked out of the dashboard (~33% of requests fail). Multiple team members are affected. No workaround exists — logging out and back in does not help.

## Recommended Fix
1. Check all backend/application server instances for consistent 'analyst' role configuration — compare role definitions, permission grants, and any related RBAC/authorization config across instances. 2. Identify which instance(s) are missing or have stale config (likely a missed deployment, failed config propagation, or cache inconsistency). 3. Redeploy or sync the role configuration to all instances. 4. Verify fix by confirming the 'analyst' role exists and has dashboard access permissions on every instance. 5. Review deployment process to ensure role/permission changes are applied atomically across all instances.

## Proposed Test Case
As a user with the 'analyst' role, make 20 sequential requests to the dashboard endpoint and assert all return 200. Run this against each backend instance individually (bypassing the load balancer) to confirm per-instance correctness.

## Information Gaps
- Exact number of backend instances and load balancer configuration (ops team has this)
- Whether the role was deployed via a migration, config file, or admin UI — and whether the deployment tool reported success on all instances
- Exact server-side error logs corresponding to the 403 responses (would confirm whether it's a missing role, missing permission grant, or authorization cache issue)
