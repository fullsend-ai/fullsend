# Triage Summary

**Title:** Intermittent 403 errors for users with 'analyst' role — authorization layer fails to recognize role on some requests

## Problem
Users assigned the 'analyst' role receive intermittent 403 Forbidden errors on any page of the dashboard. The 403 response indicates the role is not recognized. Refreshing 1-2 times clears the error. Users with admin or editor roles are not affected. The issue began around the time the analyst role was introduced and a platform update was deployed.

## Root Cause Hypothesis
The 'analyst' role is not consistently recognized by the authorization layer. Most likely causes: (1) a permissions cache that intermittently serves stale data missing the analyst role definition, (2) an inconsistent deployment across load-balanced application servers where some instances have the updated role configuration and others do not, or (3) a race condition where the role lookup sometimes completes before the role registry is fully loaded.

## Reproduction Steps
  1. Assign a user the 'analyst' role
  2. Log in as that user and navigate to the dashboard
  3. Reload the page repeatedly (roughly 10-20 times)
  4. Observe that some requests return 403 Forbidden with a response body indicating the role is not recognized
  5. Note that refreshing again loads the same page successfully

## Environment
TaskFlow dashboard, web browser, user with 'analyst' role. Issue began approximately 2 days before report, coinciding with a platform update and the introduction of the analyst role.

## Severity: high

## Impact
All users with the 'analyst' role are affected. The dashboard is partially unusable — any page load has a significant chance (estimated ~30-50%) of returning a 403. While a workaround exists (refreshing), it severely degrades the user experience and erodes trust in the platform.

## Recommended Fix
1. Check the role/permissions configuration to confirm the 'analyst' role is fully defined in all authorization layers. 2. If using multiple app server instances, verify the role definition is deployed consistently across all instances. 3. Inspect any permissions caching layer — check TTL, invalidation logic, and whether the analyst role was added after the cache was populated. 4. Review the recent deployment for any configuration drift or incomplete migration of role definitions. 5. As an immediate mitigation, consider clearing or invalidating the permissions cache.

## Proposed Test Case
Create an integration test that assigns a user the 'analyst' role and performs 50+ sequential authenticated requests to various dashboard endpoints, asserting that none return 403. Run this test against a load-balanced environment to verify consistency across instances.

## Information Gaps
- Exact error response body text (reporter paraphrased but did not copy it)
- Server-side logs for 403 responses — would confirm which authorization check is failing
- Whether the platform update specifically included changes to the role/permissions system
- Exact number and configuration of application server instances behind the load balancer
