# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard load for users with 'analyst' role since recent deployment

## Problem
Users with the newly-introduced 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 initial dashboard page loads. The error page appears to be a plain nginx 403 rather than a styled TaskFlow error page. Refreshing the page typically resolves the error immediately. The issue began coinciding with a deployment a few days ago that introduced the 'analyst' role. Users with 'editor' or 'admin' roles do not appear to be affected.

## Root Cause Hypothesis
The recent deployment that introduced the 'analyst' role likely has an inconsistent authorization configuration across backend instances or nginx upstream servers. Some instances correctly authorize the analyst role for dashboard access while others do not, causing the load balancer to intermittently route analyst-role requests to misconfigured instances. The plain nginx error page (rather than a TaskFlow-styled one) suggests the request is being rejected at the reverse proxy or gateway layer before reaching the application, pointing to an nginx configuration issue (e.g., a role-based access rule or auth middleware that doesn't recognize the new analyst role).

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the TaskFlow dashboard
  3. If the page loads successfully, refresh the page
  4. Repeat — roughly 1 in 3 loads should return a 403 Forbidden (plain nginx error page)
  5. Refreshing after a 403 should typically load the page successfully

## Environment
Affects users with the 'analyst' role across at least one team. Multiple analysts report the issue. The deployment introducing the analyst role occurred approximately 2–3 days before the initial report. Error page is plain/unstyled, likely nginx-generated.

## Severity: high

## Impact
All users with the 'analyst' role are affected. Approximately 1 in 3 page loads fail. A refresh workaround exists, so it is not a total blocker, but it significantly degrades the experience and erodes trust in the application. If the analyst role is being broadly rolled out, the number of affected users will grow.

## Recommended Fix
1. Review the recent deployment diff for changes to nginx configuration, authorization middleware, or role-permission mappings. 2. Check whether all upstream instances/containers received the updated configuration that recognizes the 'analyst' role — look for stale instances or a rolling deployment that didn't fully complete. 3. Inspect nginx access logs filtered by 403 status and correlate with the upstream instance that served each request to confirm the multi-instance hypothesis. 4. Ensure the 'analyst' role is included in whatever access-control list or authorization rule governs dashboard access at the nginx/gateway layer.

## Proposed Test Case
Automated test: Authenticate as a user with the 'analyst' role and make 20 sequential requests to the dashboard endpoint. Assert that all 20 return 200. Run this against each upstream instance individually (bypassing the load balancer) to identify any misconfigured instance.

## Information Gaps
- Exact deployment changeset that introduced the analyst role has not been identified
- Server-side logs have not been inspected to confirm which layer is returning the 403
- Number of upstream instances and whether they all have consistent configuration is unknown
- Whether other endpoints beyond the dashboard are also affected for analysts has not been explored
