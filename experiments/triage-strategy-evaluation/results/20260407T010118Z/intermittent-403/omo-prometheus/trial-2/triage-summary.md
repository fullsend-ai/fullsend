# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with 'analyst' role due to inconsistent backend permission configuration

## Problem
Users assigned the recently-introduced 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 dashboard page loads. The errors are intermittent and evenly distributed (not clustered), consistent with load-balancer routing across multiple backend servers where one or more servers lack the correct permission mapping for the analyst role. Users with the 'editor' role are completely unaffected.

## Root Cause Hypothesis
A recent deployment that introduced or configured the 'analyst' role did not fully roll out across all backend servers behind the load balancer. Approximately 1 out of 3 backend instances is missing the analyst role in its permission/RBAC configuration, causing requests routed to that instance to return 403. This explains the ~30% failure rate with even distribution and no session/auth disruption on successful retries.

## Reproduction Steps
  1. Log in as a user with the 'analyst' role
  2. Navigate to the TaskFlow dashboard
  3. Refresh the page 10+ times
  4. Observe that approximately 1 in 3 loads returns a 403 Forbidden error
  5. Note that no re-authentication is required — subsequent refreshes may succeed immediately

## Environment
Production environment, affects all users with the 'analyst' role. Started approximately 2 days ago coinciding with a deployment and the introduction of the analyst role. Multiple team members confirmed affected. Users on the 'editor' role are unaffected.

## Severity: high

## Impact
All users assigned the 'analyst' role are affected. They experience intermittent inability to access the dashboard (~30% of requests fail), forcing repeated page refreshes. This degrades productivity and trust in the platform for this user group. No workaround exists other than retrying.

## Recommended Fix
1. Check how many backend server instances are running and compare their RBAC/permission configurations for the 'analyst' role. Look for instances that were missed during the recent deployment. 2. Verify the deployment rollout status — check if any instances are running an older version or config that predates the analyst role definition. 3. Once the misconfigured instance(s) are identified, redeploy or update the permission configuration. 4. Consider adding the RBAC/role configuration to a shared config store or database rather than per-instance config to prevent future inconsistencies during rolling deployments.

## Proposed Test Case
After fix: Authenticate as an analyst-role user and make 50 consecutive requests to the dashboard endpoint. Assert that all 50 return 200. Additionally, add an integration test that verifies all defined roles (including 'analyst') are recognized and authorized for their expected endpoints across all backend instances.

## Information Gaps
- Exact number of backend server instances behind the load balancer
- Whether the analyst role is defined in per-instance config vs. a shared database
- Exact deployment tool and rollout strategy used (rolling update, blue-green, etc.)
- Whether server access logs confirm the 403s correlate with a specific instance
