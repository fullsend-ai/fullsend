# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard after role change to 'analyst' (self-hosted, load-balanced)

## Problem
Users with the 'analyst' role intermittently receive 403 Forbidden errors when accessing the TaskFlow dashboard. The issue affects approximately 1 in 3 page loads, and refreshing sometimes resolves it. Multiple team members who were moved to the same role are experiencing the issue. It began a couple of days ago, coinciding with role reassignments.

## Root Cause Hypothesis
In a load-balanced self-hosted deployment, the 'analyst' role permissions are likely inconsistent across backend instances. When a request hits an instance with stale or incomplete permission configuration for the new role, it returns 403. This explains the intermittent nature (round-robin or random LB routing) and the correlation with the role change. Possible causes: (1) permission/RBAC cache not invalidated uniformly across instances after the role was configured, (2) the role was only configured on some instances or database replicas, or (3) a session-sticky vs. round-robin LB mismatch causing some requests to hit uncached authorization paths.

## Reproduction Steps
  1. Log in to TaskFlow as a user with the 'analyst' role
  2. Navigate to the main dashboard URL
  3. Observe that the page loads successfully
  4. Refresh the page repeatedly (approximately 5-10 times)
  5. Observe that roughly 1 in 3 attempts returns a 403 Forbidden page
  6. Note that refreshing again after a 403 often succeeds

## Environment
Chrome (latest), Windows 11, TaskFlow 2.3.1, self-hosted deployment with load balancer

## Severity: high

## Impact
Multiple users on the 'analyst' role are blocked from reliably accessing the dashboard, disrupting their workflow. Approximately 33% of page loads fail. This affects the entire team that was recently reassigned to the 'analyst' role.

## Recommended Fix
1. Check permission/RBAC configuration for the 'analyst' role across all backend instances behind the load balancer. 2. Inspect whether authorization caches are being invalidated consistently across instances when roles are updated. 3. Compare the permission grants for 'analyst' on each instance or database replica. 4. Check load balancer session affinity settings — if round-robin, a per-instance cache inconsistency would produce exactly this pattern. 5. As a quick mitigation, restart all backend instances to force a fresh permission cache load.

## Proposed Test Case
Assign a test user to the 'analyst' role, then make 20+ sequential requests to the dashboard endpoint through the load balancer and verify all return 200. Repeat after clearing caches and restarting individual instances to confirm consistency.

## Information Gaps
- Server-side access logs showing which backend instance serves the 403 vs. 200 responses
- Exact load balancer configuration (round-robin, sticky sessions, etc.)
- Whether other roles are also affected or only 'analyst'
- Whether the 'analyst' role existed before or was newly created when users were reassigned
