# Triage Summary

**Title:** Intermittent 403 Forbidden errors for users with 'analyst' role across all pages

## Problem
Users assigned the newly introduced 'analyst' role are experiencing intermittent 403 Forbidden responses on all TaskFlow pages. Approximately one-third of page loads return a 403, and a simple browser refresh typically clears the error. The issue began ~2 days ago, coinciding with both a deployment/update and bulk assignment of the 'analyst' role. No users with admin or editor roles have reported the issue.

## Root Cause Hypothesis
The most likely cause is a permission/authorization caching inconsistency related to the new 'analyst' role. Two plausible mechanisms: (1) An authorization cache (server-side or CDN/reverse proxy layer) intermittently serves stale 'deny' responses because the analyst role's permissions were added but the cache was not fully invalidated. (2) A rolling deployment left some backend instances with the updated role-permission mappings and others without, so requests are allowed or denied depending on which instance handles them. The refresh-fixes-it behavior supports both theories — a new request may hit a different cache entry or backend node.

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the TaskFlow dashboard or any page
  3. Refresh the page repeatedly (approximately 10-15 times)
  4. Observe that roughly 1 in 3 loads returns a 403 Forbidden
  5. Compare behavior with a user who has the 'admin' or 'editor' role — they should not see 403s

## Environment
TaskFlow web application, accessed via browser. Issue affects the 'analyst' role specifically. A deployment or infrastructure change occurred approximately 2 days ago. Exact server/infrastructure details unknown from reporter.

## Severity: high

## Impact
All users with the 'analyst' role are affected. Roughly one-third of page loads fail. Workaround exists (refresh the page), but it degrades productivity and trust. If the analyst role is being rolled out broadly, impact will scale with adoption.

## Recommended Fix
1. Check the authorization/permission configuration for the 'analyst' role — verify it has explicit 'allow' entries for all dashboard and page endpoints, not just the absence of 'deny'. 2. Inspect any permission-caching layer (Redis, in-memory cache, CDN rules) for stale entries or inconsistent TTLs related to role lookups. 3. If running multiple backend instances, verify all instances are on the same deployment version with identical role-permission mappings. 4. Review the deployment that coincided with the role rollout for any partial or failed migration of permission data.

## Proposed Test Case
Automated test: authenticate as an analyst-role user and make 50 sequential requests to the dashboard endpoint. Assert that all 50 return 200. Run the same test against admin and editor roles as a control. Additionally, add a unit/integration test that verifies the analyst role resolves to the correct permission set on every authorization check (no cache dependency).

## Information Gaps
- Exact deployment changes made ~2 days ago (deployment logs, changelog)
- Whether the application uses a caching layer for authorization checks and its current configuration
- Whether multiple backend instances are running and if all are on the same version
- Exact number of analysts affected across the organization
- Server-side logs or request IDs from failed 403 responses
