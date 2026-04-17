# Triage Summary

**Title:** Intermittent 403 Forbidden errors across all pages for multiple users (~33% of requests)

## Problem
Multiple team members are experiencing intermittent 403 Forbidden errors on any page in TaskFlow. The errors occur roughly 1 in 3 page loads, are not tied to any specific action or page, and resolve on refresh. The error is served by the application itself (TaskFlow-styled error page, not a proxy). The issue started approximately two days ago with no known permission changes.

## Root Cause Hypothesis
One or more application instances behind a load balancer are misconfigured or running a bad deployment, causing them to reject valid authenticated requests with a 403. The ~33% failure rate suggests roughly one out of N instances is affected. A recent deployment or configuration change (approximately two days ago) likely introduced the issue on a subset of instances.

## Reproduction Steps
  1. Log in to TaskFlow as any team member
  2. Load or refresh any page (dashboard, project view, etc.)
  3. Repeat several times — approximately 1 in 3 loads will return a 403 Forbidden error
  4. Refreshing the same page after a 403 will typically succeed

## Environment
Production environment, affects multiple users across the team, started approximately 2 days ago. Error is application-level (TaskFlow-branded error page).

## Severity: high

## Impact
All team members are affected. Roughly 33% of page loads fail, significantly disrupting workflow. Users can work around it by refreshing, but the experience is degrading trust and productivity.

## Recommended Fix
1. Check recent deployments from ~2 days ago for changes to auth middleware, session handling, or permission logic. 2. Inspect all application instances behind the load balancer — compare configs and deployed versions to identify any inconsistency. 3. Check application logs on each instance for 403 responses to correlate which instance(s) are generating them. 4. If an instance is running a bad version or config, redeploy or restart it. 5. Verify session/token validation is consistent across all instances (e.g., shared session store, consistent JWT secret).

## Proposed Test Case
Send 20+ sequential authenticated requests to the dashboard endpoint and verify all return 200. Repeat while pinning requests to each individual backend instance to confirm none return 403 for valid sessions.

## Information Gaps
- Exact deployment or configuration change that occurred ~2 days ago
- Number of application instances behind the load balancer
- Server-side logs showing which instance(s) generate the 403 responses
- Whether the 403 response body contains any specific error code or message beyond the styled page
