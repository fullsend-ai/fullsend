# Triage Summary

**Title:** Intermittent 403 on dashboard for users with the new 'analyst' role — likely permissions cache inconsistency

## Problem
Users assigned the recently-created 'analyst' role are intermittently receiving 403 Forbidden errors when loading the TaskFlow dashboard. The errors come from the application's own authorization layer (branded error page). Refreshing a few times usually resolves it temporarily. Admin-role users are not affected. The issue began approximately one day after the analyst role was assigned, and has persisted for about two days.

## Root Cause Hypothesis
The application's permission/authorization cache is inconsistently resolving the 'analyst' role's access to the dashboard resource. Most likely scenario: the analyst role's dashboard permission is either (a) not fully propagated across all app server instances or cache nodes, or (b) subject to a cache eviction/rebuild race where stale entries intermittently deny access. The one-day delay between role assignment and symptom onset is consistent with a cache TTL expiring and the role's permissions not being correctly re-cached on every rebuild cycle.

## Reproduction Steps
  1. Assign a user the 'analyst' role in TaskFlow
  2. Wait approximately 24 hours (or until the permissions cache cycles)
  3. Navigate to the TaskFlow dashboard repeatedly
  4. Observe that some loads return 403 ('You don't have permission to access this resource') while others succeed
  5. Confirm that switching the same user to an admin role eliminates the 403s

## Environment
TaskFlow production environment. Affects all users with the new 'analyst' role. No specific browser, OS, or network dependency identified — the issue is server-side.

## Severity: high

## Impact
All users assigned the 'analyst' role are intermittently locked out of the dashboard, which is likely a primary workspace. Multiple team members are affected. There is a partial workaround (refreshing), but it degrades productivity and trust in the application.

## Recommended Fix
1. Inspect the permission/role definitions for the 'analyst' role — verify that dashboard access is explicitly granted and not relying on implicit or inherited permissions that may not resolve consistently. 2. Examine the authorization cache layer (in-memory cache, Redis, etc.) for how role-to-permission mappings are stored and invalidated — look for inconsistencies across app server instances if load-balanced. 3. Check whether the cache rebuild/hydration logic correctly picks up all permissions for newly-created roles vs. legacy roles. 4. As an immediate mitigation, consider clearing the permissions cache or forcing a full re-sync of analyst role permissions across all instances.

## Proposed Test Case
Create an integration test that: (1) creates a new role with dashboard access, (2) assigns it to a test user, (3) makes 50+ sequential dashboard requests as that user across different app instances (if applicable), and (4) asserts that zero requests return 403. Additionally, add a test that invalidates the permissions cache mid-sequence and verifies access is maintained after cache rebuild.

## Information Gaps
- Exact number of app server instances and whether a load balancer distributes requests across them (would explain why some requests succeed and others fail)
- The specific caching mechanism used for permission resolution (in-memory, distributed cache, DB query cache)
- Whether any deployment or configuration change coincided with the onset — the reporter lacked visibility into this
- Exact cache TTL and invalidation strategy for role-permission mappings
