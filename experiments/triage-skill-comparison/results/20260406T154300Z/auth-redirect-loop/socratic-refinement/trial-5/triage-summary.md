# Triage Summary

**Title:** Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra but are immediately redirected back to the login flow upon returning to TaskFlow. Affected users fall into two categories: those using plus-addressing in their email (e.g., jane+taskflow@company.com) and those whose email address changed or was aliased over time. The issue is browser-independent but occasionally does not reproduce in incognito windows.

## Root Cause Hypothesis
Two interacting problems: (1) **Email claim mismatch:** Entra ID returns a different email claim format than Okta did — likely normalizing plus-addressed emails (stripping the +tag) or returning the primary alias rather than the alias Okta used. When TaskFlow's user-lookup compares the incoming OIDC email claim against its stored user records, it fails to find a match for these users, causing the authentication callback to reject the session and restart the login flow. (2) **Stale session cookies:** Residual Okta session cookies or cached TaskFlow auth tokens in normal browsers interfere with the new Entra flow — the app may detect an existing (now-invalid) session, attempt to validate it, fail, and initiate a new login, creating the loop. Incognito works occasionally because it starts with no cached state.

## Reproduction Steps
  1. Set up a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com) or an aliased email that differs from the Entra ID primary email
  2. Ensure the user has previously logged in via the old Okta SSO (so browser has cached cookies/tokens)
  3. Attempt to log in via the new Entra ID SSO flow in a normal browser session
  4. Observe: user authenticates successfully at Microsoft but is redirected back to TaskFlow's login page, which immediately redirects back to Microsoft, creating an infinite loop
  5. Optionally: clear all TaskFlow and Okta cookies, then retry — this may resolve the loop for the session but fail again if the email mismatch persists

## Environment
TaskFlow with OIDC/SAML SSO integration, recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox equally. Company has users with plus-addressed emails and historically aliased/changed email addresses.

## Severity: high

## Impact
~30% of the team is unable to log into TaskFlow in their normal browser, blocking their daily work. Workarounds (incognito) are unreliable and not sustainable.

## Recommended Fix
Investigate two areas: (1) **User matching logic:** Examine how TaskFlow resolves the authenticated user from the OIDC/SAML callback. Check which claim is used for lookup (email, sub, preferred_username, etc.) and compare the claim values Entra returns against what is stored in the TaskFlow user database. For plus-addressed users, check whether Entra strips the +tag or returns a different canonical form than Okta did. Consider matching on a stable, provider-agnostic identifier (like `sub` or `oid`) rather than email, or implement email normalization/alias-aware matching. (2) **Session/cookie cleanup:** Add migration logic that invalidates old Okta session cookies on first Entra login attempt, or explicitly clear the TaskFlow auth session when the IdP configuration has changed. Check for cookie domain/path conflicts between old and new SSO flows.

## Proposed Test Case
Create integration tests for the SSO callback handler that simulate Entra ID returning: (a) a plus-addressed email matching the stored email, (b) a plus-addressed email with the +tag stripped, (c) a primary alias that differs from the stored email. Verify that in all cases the correct user is resolved and a valid session is created without redirect. Additionally, test that a browser with stale Okta session cookies can complete the Entra login flow without entering a redirect loop.

## Information Gaps
- Exact OIDC/SAML claim(s) TaskFlow uses for user matching (email vs sub vs UPN)
- Whether Entra ID is configured to return the email or the UPN in the token, and what format each has for plus-addressed users
- Whether clearing all cookies fully resolves the issue for affected users or if the email mismatch alone is sufficient to cause the loop
- Whether TaskFlow has a user provisioning/sync mechanism that could be used to update stored email values
