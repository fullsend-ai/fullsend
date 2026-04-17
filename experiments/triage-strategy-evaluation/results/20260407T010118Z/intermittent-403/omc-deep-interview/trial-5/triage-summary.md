# Triage Summary

**Title:** Intermittent nginx-level 403 errors for users with the new analyst role — likely inconsistent role config across load-balanced instances

## Problem
Users assigned the newly created 'analyst' role receive intermittent 403 Forbidden errors (roughly 1 in 3 page loads) across all TaskFlow pages. The 403 is served by nginx (plain error page, not application-styled), and a page refresh typically resolves it temporarily. Users with the 'editor' role are unaffected. The issue began approximately two days ago, coinciding with the analyst role's creation.

## Root Cause Hypothesis
TaskFlow runs behind a load balancer with multiple nginx instances (or upstream servers). When the analyst role was created, its permission/access configuration was applied to some but not all instances. Requests routed to a correctly configured instance succeed; requests routed to an unconfigured instance return a 403 from nginx before the request ever reaches the application. The ~1-in-3 failure rate is consistent with one of three backend servers missing the configuration.

## Reproduction Steps
  1. Log in as a user with the analyst role
  2. Navigate to the TaskFlow dashboard or any other page
  3. Refresh the page repeatedly (roughly 5-10 times)
  4. Observe that approximately 1 in 3 loads returns a plain nginx 403 Forbidden page
  5. Confirm that the same pages load consistently for a user with the editor role

## Environment
Production TaskFlow instance, users with the analyst role (created ~2 days ago), nginx reverse proxy / load balancer in front of the application

## Severity: high

## Impact
All users with the analyst role are affected. They can work around the issue by refreshing the page, but the frequent 403 errors significantly disrupt their workflow. The number of affected users is at least two but likely includes all analysts.

## Recommended Fix
1. Check the nginx configuration across all load-balanced instances/servers for analyst role access rules — look for inconsistencies. 2. If using a config management or deployment tool, verify the analyst role config was deployed to all instances. 3. Reload or re-deploy the corrected nginx configuration to all instances. 4. If permissions are managed at the API gateway or reverse proxy layer (e.g., nginx auth_request, map directives, or upstream ACLs), ensure the analyst role is recognized consistently. 5. Verify the fix by logging in as an analyst-role user and confirming no 403s across 20+ page loads.

## Proposed Test Case
Automated test: Make 50 sequential authenticated HTTP requests as an analyst-role user to the dashboard endpoint and assert that all return 200. Run this against each individual backend instance (bypassing the load balancer) to confirm per-instance correctness.

## Information Gaps
- Exact number of backend nginx instances behind the load balancer
- How the analyst role permissions are configured at the nginx layer (auth_request module, map directive, external auth service, etc.)
- Total number of users affected (only two confirmed so far)
- Whether the 403 response includes any additional headers or body content beyond the plain error text
