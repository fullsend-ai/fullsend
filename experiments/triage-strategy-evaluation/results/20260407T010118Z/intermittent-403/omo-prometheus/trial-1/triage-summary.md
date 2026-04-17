# Triage Summary

**Title:** Intermittent 403 errors for users with 'analyst' role due to likely inconsistent deployment across backend instances

## Problem
Users assigned the newly-added 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 page loads across all application pages (dashboard, reports, settings). Refreshing one or more times resolves the error temporarily. Multiple team members with the analyst role are affected. The issue began shortly after a deployment that introduced the analyst role.

## Root Cause Hypothesis
The deployment that added the 'analyst' role was not applied consistently across all 3 backend server instances behind the load balancer. Instances that received the update recognize the analyst role and grant access; instances that did not receive (or fully apply) the update do not recognize the role and return 403. Round-robin or similar load balancing distributes requests across all instances, producing the intermittent ~1-in-3 failure pattern.

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to any page (dashboard, reports, or settings)
  3. Observe that approximately 1 in 3 page loads returns a 403 Forbidden
  4. Refresh the page — it will usually succeed on retry (routed to a different instance)

## Environment
TaskFlow web application behind nginx with 3 backend server instances and a load balancer. A recent deployment introduced a new 'analyst' role. Standard (non-custom) 403 error page is returned.

## Severity: high

## Impact
All users with the 'analyst' role are intermittently locked out of the entire application. This appears to affect an entire team that was recently migrated to the new role. Users can work around it by refreshing, but the experience is disruptive and unreliable.

## Recommended Fix
1. Immediately verify the deployment version running on all 3 backend instances — confirm whether they are running the same version. 2. If version skew is found, redeploy the latest version to the out-of-date instance(s). 3. Review the analyst role's permission/authorization configuration to ensure it grants access to all standard application routes. 4. After redeployment, verify the fix by making multiple requests as an analyst-role user and confirming no 403s occur. 5. Investigate why the deployment was not applied to all instances (CI/CD pipeline issue, rolling deploy failure, etc.) to prevent recurrence.

## Proposed Test Case
Authenticate as an analyst-role user and make 20+ sequential requests to various application endpoints (dashboard, reports, settings). Assert that all requests return 200 and none return 403. Optionally, pin requests to each backend instance individually and verify consistent 200 responses from all instances.

## Information Gaps
- Exact deployment versions running on each of the 3 backend instances (reporter is asking ops team)
- Whether the 403 originates from the application layer or from nginx (response headers not yet captured)
- Whether users with non-analyst roles (admin, editor) are definitively unaffected (reporter is checking with teammates)
