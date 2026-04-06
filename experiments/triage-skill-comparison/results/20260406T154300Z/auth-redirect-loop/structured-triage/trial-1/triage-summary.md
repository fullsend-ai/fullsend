# Triage Summary

**Title:** SSO redirect loop after Okta-to-Entra migration for users with plus-addressed or aliased emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Microsoft but are immediately redirected back to the login flow. The affected population correlates with users who have plus-addressed emails (e.g., jane+taskflow@company.com) or emails that changed/were aliased during the Entra setup.

## Root Cause Hypothesis
TaskFlow likely matches the SSO identity to a local user account by comparing the email claim from the IdP against the stored user email. Entra ID is probably returning a normalized or primary email address in its claims (e.g., jane@company.com) that does not exactly match the plus-addressed or aliased email stored in TaskFlow's user table (e.g., jane+taskflow@company.com). When the lookup fails, TaskFlow cannot establish a session and restarts the auth flow, causing the loop. The intermittent success in incognito/after clearing cookies suggests residual Okta session cookies may also interfere with the new auth flow.

## Reproduction Steps
  1. Deploy TaskFlow v2.3.1 with Entra ID SSO configured
  2. Create or have a user account in TaskFlow whose stored email is a plus-addressed variant (e.g., user+taskflow@company.com) or an old aliased email
  3. Ensure the corresponding Entra ID account's primary email differs from the TaskFlow-stored email
  4. Attempt to log in via SSO
  5. Observe: user authenticates with Microsoft successfully but is redirected back to the login page in a loop

## Environment
TaskFlow v2.3.1, self-hosted. Affects users on Windows 10/11 and macOS across Chrome, Edge, and Firefox. Not browser- or OS-specific.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. No consistent workaround exists. This blocks affected users from accessing the application entirely.

## Recommended Fix
1. Inspect the SAML assertion or OIDC token claims from Entra ID to confirm which email field is being returned (e.g., `email`, `preferred_username`, `upn`). 2. Compare that value against what TaskFlow has stored for affected users. 3. Fix the user-matching logic to normalize email comparison — strip plus-address suffixes and/or match against known aliases. 4. Consider adding a migration script or admin tool to reconcile stored emails with Entra ID identities. 5. Investigate and clear any residual Okta-related session cookies that may interfere with the new auth flow.

## Proposed Test Case
Create test users with plus-addressed emails and aliased emails in TaskFlow. Configure Entra ID to return their primary (non-plus, non-aliased) email in the token claims. Verify that SSO login succeeds and correctly matches these users to their existing accounts without entering a redirect loop.

## Information Gaps
- No server-side logs or browser network traces showing the exact redirect sequence or any error responses
- The specific OIDC/SAML claim field TaskFlow uses for user matching has not been confirmed
- Whether TaskFlow's auth configuration has a specific email claim mapping setting is unknown
