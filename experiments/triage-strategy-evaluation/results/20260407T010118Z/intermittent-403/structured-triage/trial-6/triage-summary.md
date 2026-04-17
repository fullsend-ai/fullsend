# Triage Summary

**Title:** Intermittent 403 Forbidden on main dashboard for users with new analyst role

## Problem
Users assigned to the recently introduced 'analyst' role are receiving intermittent 403 Forbidden errors when accessing the main dashboard. The errors occur roughly 1 in 3 page loads and can be resolved by refreshing. Users on the older 'editor' role are unaffected. The issue began approximately two days ago, coinciding with when users were switched to the analyst role.

## Root Cause Hypothesis
The new analyst role likely has incorrect or incomplete dashboard permissions, and the intermittent nature (roughly 1-in-3 failure rate) suggests the application runs behind a load balancer with multiple backend instances where one or more instances have a stale or misconfigured permissions/role definition that does not recognize the analyst role's dashboard access. Alternatively, a caching layer may be intermittently serving a cached authorization decision.

## Reproduction Steps
  1. Assign a user to the 'analyst' role in TaskFlow 2.3.1
  2. Navigate to the main dashboard (e.g., via bookmark or direct URL)
  3. Observe that the page intermittently returns 403 Forbidden (~1 in 3 loads)
  4. Refresh the page — it typically loads on retry

## Environment
TaskFlow 2.3.1, observed on Windows 10 / Chrome (latest) and Firefox, not browser-specific. Affects multiple users on the analyst role.

## Severity: high

## Impact
All users assigned to the new analyst role are intermittently blocked from accessing the main dashboard. This affects their core workflow and creates confusion about account status. Multiple team members are impacted.

## Recommended Fix
1. Inspect the permissions/RBAC configuration for the analyst role and verify it includes explicit dashboard access grants. 2. Check whether all application server instances (behind the load balancer) have the same, up-to-date role definitions — a rolling deploy or config sync issue may have left one or more instances without the analyst role definition. 3. Review any authorization caching layer for stale entries. 4. Check server-side access logs filtered by 403 status to confirm the correlation with analyst-role sessions and identify which backend instance(s) are rejecting requests.

## Proposed Test Case
As an analyst-role user, load the main dashboard 20 times in succession and verify 0 responses return 403. Repeat after any deployment or config change to ensure consistency across all backend instances.

## Information Gaps
- Server-side access logs and which backend instance(s) return the 403
- Exact RBAC/permissions configuration for the analyst role vs. the editor role
- Whether the analyst role was added via a recent deployment or a runtime config change
