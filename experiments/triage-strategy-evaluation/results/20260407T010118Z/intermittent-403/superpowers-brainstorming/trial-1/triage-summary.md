# Triage Summary

**Title:** Intermittent 403 Forbidden on dashboard for users with new analyst role

## Problem
Users assigned the recently-introduced 'analyst' role are getting 403 Forbidden errors on approximately 1 in 3 dashboard page loads. The errors are non-deterministic — refreshing the page will eventually succeed. Users on admin and editor roles are unaffected. The analyst role was rolled out about a week ago, and the 403 errors began appearing roughly 2 days ago.

## Root Cause Hypothesis
The dashboard permission grant for the analyst role is inconsistent across application server instances or permission cache nodes. When a request hits an instance with the correct configuration, it succeeds; when it hits one with a stale or missing grant, it returns 403. The gap between the role rollout (~1 week ago) and symptom onset (~2 days ago) suggests a subsequent deployment or cache invalidation event introduced the inconsistency.

## Reproduction Steps
  1. Assign a test user the 'analyst' role
  2. Log in as that user and navigate to the dashboard
  3. Refresh the page 10–15 times and observe that some loads return 403 while others succeed
  4. Repeat with an admin or editor user and confirm the 403 does not occur

## Environment
Production environment, multiple team members affected, all on the new analyst role. No specific browser or OS dependency reported (consistent with a server-side issue).

## Severity: high

## Impact
All users on the analyst role are affected. They can work around it by refreshing, but it degrades productivity and trust in the application. The analyst role appears to be a recent addition being rolled out to teams, so the affected population may be growing.

## Recommended Fix
1. Audit the analyst role's permission configuration across all app server instances / permission cache layers to find inconsistencies in the dashboard access grant. 2. Check for recent deployments or config changes ~2 days ago that may have caused drift. 3. If using distributed permission caching, force a cache refresh or re-sync across all nodes. 4. Verify the analyst role's permission set matches the intended specification in a single authoritative source.

## Proposed Test Case
Automated test: authenticate as an analyst-role user and request the dashboard endpoint N times (e.g., 50) in succession, asserting 200 on every request. Run this against each individual app server instance to isolate which nodes return 403.

## Information Gaps
- Exact number of app server instances and whether requests are load-balanced (internal infrastructure detail)
- Whether a specific deployment or config change occurred ~2 days ago that correlates with symptom onset
- Whether any other pages/endpoints besides the dashboard are affected for analyst-role users (reporter only mentioned dashboard)
