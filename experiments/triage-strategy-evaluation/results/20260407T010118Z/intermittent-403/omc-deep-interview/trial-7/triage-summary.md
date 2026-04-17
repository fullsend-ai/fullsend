# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with 'analyst' role

## Problem
Users assigned the 'analyst' role receive full-page 403 Forbidden errors on approximately one-third of dashboard page loads. The error occurs across all dashboard sections, not tied to a specific panel or endpoint. Refreshing the page typically resolves it until the next occurrence. The issue began around the time the analyst role was introduced (roughly 1-2 weeks ago). Users with 'admin' or 'editor' roles are unaffected.

## Root Cause Hypothesis
The analyst role's permissions are likely being evaluated inconsistently across requests. Most probable causes: (1) The application runs behind a load balancer with multiple backend instances, and one or more instances have a stale or incomplete permissions/RBAC configuration that doesn't fully recognize the analyst role — requests that hit a correctly-configured instance succeed, while those hitting a misconfigured one return 403. (2) There is a caching layer (e.g., session cache, permissions cache) with a short TTL or race condition that intermittently drops or fails to resolve analyst role permissions. (3) The analyst role was added but a related permission grant (e.g., dashboard access scope) was partially applied, and a non-deterministic permission check (such as checking multiple permission sources) sometimes misses it.

## Reproduction Steps
  1. Create or use a test account with only the 'analyst' role assigned
  2. Log into TaskFlow and navigate to the dashboard
  3. Repeatedly navigate between dashboard sections or refresh the dashboard page
  4. Within approximately 3-5 attempts, a full-page 403 Forbidden error should appear
  5. Compare behavior with an 'editor' or 'admin' role account — those should never see the 403

## Environment
Chrome browser, office network (no VPN/proxy), affects multiple analyst-role users on the same team. Issue started approximately 1-2 weeks ago coinciding with the analyst role being granted to a batch of users.

## Severity: high

## Impact
All or most users with the 'analyst' role are affected. At least 3 confirmed reporters. The issue disrupts normal workflow — users must repeatedly refresh to regain access. No data loss, but significant productivity impact and poor user experience. Workaround exists (refresh the page) but is not acceptable long-term.

## Recommended Fix
1. Inspect the RBAC/permissions configuration for the 'analyst' role — verify the dashboard access permission is correctly and completely assigned. 2. If running multiple application instances behind a load balancer, check that all instances have the same permissions configuration (look for config drift or failed deployments). 3. Examine any permission caching layer — check for race conditions, short TTLs, or cache invalidation issues that could cause intermittent permission check failures. 4. Review the authorization middleware/interceptor to see if the 403 decision is logged — correlate 403 responses with the specific permission check that failed and which server instance handled the request. 5. Check if the analyst role was added to the database/config but a dependent migration or cache warm-up was incomplete.

## Proposed Test Case
Create an integration test that: (1) assigns the 'analyst' role to a test user, (2) makes 50+ sequential authenticated requests to the dashboard endpoint, (3) asserts that all requests return 200. Run this test against each application instance individually (bypassing the load balancer) to identify if the issue is instance-specific. Additionally, add a unit test for the permission resolver that verifies the analyst role is granted dashboard access under all code paths.

## Information Gaps
- Which specific authorization check or middleware is returning the 403 (server-side logs not yet examined)
- Whether the issue is instance-specific in a load-balanced setup (not yet confirmed if multiple instances exist)
- Whether logging out and back in (forcing a fresh session) prevents the 403 vs. just refreshing
- Whether incognito mode changes the behavior (could indicate cookie/session-related cause)
- Exact server-side error or permission check that fails (reporter was unable to check browser dev tools)
