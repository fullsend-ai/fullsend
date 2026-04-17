# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with new 'analyst' role — likely inconsistent deployment across backend instances

## Problem
Users with the recently deployed 'analyst' role receive full-page 403 errors (TaskFlow's own error page) approximately 1 in 3 times when loading the dashboard. Refreshing usually resolves it. The issue began ~2 days ago, coinciding with the deployment of the new analyst role. Multiple analysts on the same data team are affected.

## Root Cause Hypothesis
The ~1-in-3 failure rate suggests the application runs behind a load balancer with multiple backend instances (likely 3), and the analyst role's dashboard permissions were not deployed consistently to all instances. One instance lacks the correct permission grants for the analyst role, causing 403s when the load balancer routes to it. This explains why the error is intermittent (round-robin or random LB routing) rather than consistent (which a pure misconfiguration on all instances would produce).

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the dashboard URL
  3. Observe that the page loads successfully or returns a full 403 error page
  4. Refresh repeatedly — approximately 1 in 3 loads should return 403
  5. Compare behavior with an admin or editor role user on the same dashboard URL

## Environment
TaskFlow production environment, users with the new 'analyst' role, behind a load balancer with multiple backend instances. Deployment of analyst role occurred approximately 2 days before report (around April 5, 2026).

## Severity: high

## Impact
All users with the 'analyst' role are affected. The dashboard is inaccessible ~33% of the time, disrupting the data team's workflow. Workaround exists (refresh the page) but is unreliable and disruptive. Admins and editors appear unaffected, though this is not conclusively confirmed.

## Recommended Fix
1. Check all backend/app server instances for consistent analyst role permission configuration — compare role-to-route mappings across instances. 2. Identify which instance(s) return 403 for analyst users (correlate access logs with load balancer routing). 3. Redeploy or sync the analyst role permissions to all instances. 4. Review the deployment process to ensure role/permission changes are applied atomically across all instances.

## Proposed Test Case
Automated test: authenticate as an analyst-role user and request the dashboard endpoint N times (e.g., 20); assert that every request returns 200. Run this against each backend instance individually (bypassing the load balancer) to verify consistent behavior.

## Information Gaps
- Exact number of backend instances behind the load balancer
- Deployment manifest or changelog for the analyst role rollout
- Confirmation that non-analyst roles (admin, editor) are definitively unaffected
- Server-side access logs correlating 403 responses with specific backend instances
