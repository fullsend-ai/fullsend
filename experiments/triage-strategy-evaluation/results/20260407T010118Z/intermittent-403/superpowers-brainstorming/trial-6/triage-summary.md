# Triage Summary

**Title:** Intermittent 403 Forbidden errors for users on new 'analyst' role after recent permissions deployment

## Problem
Users who were recently migrated to the new 'analyst' role are experiencing intermittent 403 Forbidden errors across all dashboard pages. The errors are non-deterministic — the same page may return 403 on one request and load successfully on the next refresh. Users on the old role are not affected. The issue began immediately after a deployment that updated the role/permissions system.

## Root Cause Hypothesis
The recent deployment introduced the 'analyst' role but likely has a caching inconsistency in the authorization layer. Possible causes: (1) a permissions cache that intermittently serves stale entries that don't include the new role, (2) a race condition where the role lookup resolves before the new role's permissions are fully loaded, or (3) multiple app servers/containers where some have stale permission mappings for the new role. The fact that a refresh usually fixes it suggests a short-lived cache TTL or per-request cache miss pattern.

## Reproduction Steps
  1. Ensure a user account is assigned the new 'analyst' role
  2. Navigate to the TaskFlow dashboard
  3. Refresh the page repeatedly (may take several attempts)
  4. Observe that some requests return 403 Forbidden while others succeed
  5. Compare with a user on the old role — they should never see 403s

## Environment
Production environment, post-deployment from ~2 days ago (approx. April 5, 2026). Affects multiple users on the 'analyst' role across the same team.

## Severity: high

## Impact
All users migrated to the new 'analyst' role are affected. They can work around it by refreshing, but it disrupts workflow and erodes trust in the platform. As more users are migrated to the new role, the blast radius will grow.

## Recommended Fix
1. Check the authorization/permissions cache layer for how the 'analyst' role is resolved — look for stale cache entries or inconsistent cache invalidation after the deployment. 2. Check if multiple app server instances have different permission configurations (rolling deployment that didn't fully propagate). 3. Review the role lookup code path for race conditions where the role is recognized but its permissions aren't yet loaded. 4. As an immediate mitigation, consider flushing the permissions cache or forcing a re-deploy to ensure all instances have consistent role definitions.

## Proposed Test Case
Create an integration test that assigns a user the 'analyst' role and makes 50+ sequential authenticated requests to the dashboard endpoint, asserting that all return 200. Run this against a deployment that includes role/permission changes to catch intermittent authorization failures.

## Information Gaps
- Exact deployment identifier/timestamp to correlate with deploy logs
- Whether the authorization system uses an explicit cache (Redis, in-memory) vs. session-based checks
- Server-side error logs or access logs showing the 403 responses for correlation
