# Triage Summary

**Title:** Intermittent 403 Forbidden on all pages for users with newly-created 'analyst' role

## Problem
Users assigned the recently-created 'analyst' role experience intermittent 403 Forbidden errors on approximately 1 out of 3 page loads. The errors affect all pages (dashboard, reporting features, etc.), not a specific route. Refreshing the same URL without re-authenticating sometimes succeeds, sometimes fails. At least two users with the same role and similar assignment timing are affected.

## Root Cause Hypothesis
The 'analyst' role is new and its permission definitions are likely not consistently available across all application server instances. If the app runs behind a load balancer with N instances (the ~2/3 success rate suggests 3 instances with 1 misconfigured), requests routed to the instance(s) missing the role definition would return 403. Alternatively, a permissions cache with inconsistent TTL or a replication lag in the authorization data store could produce the same pattern. The key signal is that the failure rate is roughly constant regardless of page, time, or user action — pointing to infrastructure-level inconsistency rather than a logic bug.

## Reproduction Steps
  1. Assign a user the 'analyst' role (this is a recently-created role)
  2. Log in as that user via Chrome
  3. Attempt to load the dashboard or any other page
  4. Observe that approximately 1 in 3 loads returns a 403 Forbidden
  5. Refresh the same URL — it may succeed or fail independently of the previous attempt

## Environment
Chrome on desktop (work laptop). Multiple users affected. No specific OS version reported. Started approximately 2 days before the report, coinciding with the 'analyst' role being created and first assigned.

## Severity: high

## Impact
All users with the 'analyst' role are unable to reliably access any part of the application, including the reporting features the role was specifically created to unlock. The role appears to be new, so the affected user base is currently small (at least 2), but will grow as more users are assigned the role.

## Recommended Fix
1. Check whether the analyst role and its permission grants are consistently defined across all application server instances, authorization service replicas, or permission cache layers. 2. If using a distributed cache or database for permissions, verify replication status and check for nodes with stale data. 3. Compare the permission storage for the 'analyst' role against a working role like 'viewer' — look for missing entries in specific nodes/replicas. 4. If the role was added via a migration or config change, verify it was applied to all instances (check for failed deployments or skipped nodes). 5. As a quick validation, check access logs to see if 403s correlate with a specific upstream server or instance ID.

## Proposed Test Case
Send 20+ sequential authenticated requests as an 'analyst' user to the dashboard endpoint and assert that all return 200. If a load balancer is in use, pin requests to each backend instance individually and verify consistent 200 responses. Additionally, add an integration test that creates a new role, assigns it to a user, and verifies access across all instances.

## Information Gaps
- Exact application server topology (number of instances, load balancer configuration)
- How the 'analyst' role was provisioned (migration, admin UI, API call) and whether it completed successfully across all stores
- Whether any other recently-created roles exhibit the same behavior
- Server-side access logs correlating 403 responses with specific instance IDs
