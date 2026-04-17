# Triage Summary

**Title:** Intermittent 403 errors for users assigned the new 'analyst' role

## Problem
Users who were recently assigned the 'analyst' role (approximately 2 days ago) are experiencing random 403 Forbidden errors across all dashboard pages. The errors are not page-specific, affect multiple users with the same role, and resolve temporarily on refresh. The pattern suggests a permission evaluation inconsistency rather than a blanket denial.

## Root Cause Hypothesis
The 'analyst' role likely replaced users' previous roles (e.g., 'viewer' or 'editor'). The intermittent nature suggests either: (1) a permission/session cache that sometimes serves stale authorization decisions — some backend nodes or cache entries still reference the old role while others have the new one, or (2) the 'analyst' role's permissions were incompletely configured and a race condition in permission resolution causes sporadic failures. A load-balanced setup where not all nodes have consistent permission state would explain why refreshing sometimes works immediately and sometimes takes multiple attempts.

## Reproduction Steps
  1. Identify a user with only the 'analyst' role assigned
  2. Log in and access the dashboard repeatedly
  3. Observe that some requests return 403 while others succeed
  4. Check whether the 403 correlates with hitting different backend nodes (if load-balanced) or with cache timing

## Environment
Production TaskFlow instance, multiple users affected, all with 'analyst' role. No confirmed deployment changes, but role assignment change ~2 days ago aligns with symptom onset.

## Severity: high

## Impact
All users with the 'analyst' role are intermittently locked out of the dashboard throughout the day, disrupting their workflow. Multiple team members affected.

## Recommended Fix
1. Check the permission/role configuration for 'analyst' — verify it grants dashboard access equivalent to the roles it replaced. 2. Inspect the permission caching layer (session cache, CDN, or reverse proxy) for stale entries tied to the old role. 3. If load-balanced, check whether all backend nodes have consistent role/permission definitions. 4. Review the role migration — confirm the old role was cleanly removed and the new role's permissions were fully propagated. 5. Consider flushing permission caches for affected users as an immediate mitigation.

## Proposed Test Case
Assign a test user the 'analyst' role (removing any previous role), then issue 50+ sequential authenticated requests to the dashboard API and verify all return 200. Repeat against each backend node individually if load-balanced.

## Information Gaps
- Exact previous role name (reporter unsure if it was 'viewer' or 'editor')
- Whether the old role was explicitly removed or still coexists in the system
- Whether a deployment occurred around the same time as the role change
- Specific backend architecture details (load balancer config, caching layer)
