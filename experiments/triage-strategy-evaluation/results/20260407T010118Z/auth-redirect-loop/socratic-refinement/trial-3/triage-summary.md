# Triage Summary

**Title:** Infinite login redirect loop for users whose Entra ID email claim doesn't match stored TaskFlow email

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but TaskFlow cannot match the returned identity to a local user record, so it never establishes a session and redirects back to the IdP repeatedly.

## Root Cause Hypothesis
TaskFlow's user-lookup during the OAuth/OIDC callback matches the email claim from the identity provider against stored user records. Entra ID normalizes email addresses by stripping the sub-addressing (plus-sign) portion — so a user stored as `jane+taskflow@company.com` receives an email claim of `jane@company.com`. The lookup fails, no session is created, and the auth middleware redirects back to Entra ID, creating the loop. Users whose emails changed between the Okta and Entra configurations are affected for the same reason (stored email ≠ incoming claim). Stale Okta session cookies may contribute in some cases but are not the primary cause, since brand-new users with plus addresses also experience the loop.

## Reproduction Steps
  1. Have a user account in TaskFlow with a plus-sign email address (e.g., jane+taskflow@company.com)
  2. Configure TaskFlow SSO to use Microsoft Entra ID
  3. Ensure the Entra ID user's primary email is jane@company.com (no plus-sign portion)
  4. Attempt to log in to TaskFlow via SSO
  5. Observe: successful Entra ID authentication, redirect back to TaskFlow, immediate redirect back to Entra ID, repeating indefinitely

## Environment
TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Affects multiple browsers (Chrome, Edge, Firefox). Specific TaskFlow version not reported but issue is server-side user-lookup logic, not browser-specific.

## Severity: high

## Impact
~30% of the team cannot log in to TaskFlow at all. These are users with plus-sign email addresses or users whose email changed during the SSO migration. Complete loss of access for affected users with no reliable workaround.

## Recommended Fix
1. **Immediate fix:** Investigate the user-lookup logic in the OAuth/OIDC callback handler. Identify where the incoming email claim is matched against stored user records and add case-insensitive, sub-address-aware matching (normalize both sides by stripping the `+...` portion before the `@` for comparison purposes, or match on a stable claim like `sub`/`oid` rather than email). 2. **Session cleanup:** Invalidate all existing pre-migration sessions to eliminate stale Okta session cookies as a contributing factor. 3. **Entra ID configuration:** Alternatively or additionally, configure optional claims in the Entra ID app registration to emit the full email with plus-addressing, or map a custom claim. 4. **Data reconciliation:** For users whose email changed between Okta and Entra, update the stored email or add a secondary identifier lookup.

## Proposed Test Case
Create a test user in TaskFlow with email `testuser+tag@company.com`. Configure the mock/test IdP to return the email claim as `testuser@company.com`. Verify that the OAuth callback successfully matches the user, creates a valid session, and redirects to the dashboard without looping. Also verify that a user with a matching email (no plus-sign discrepancy) continues to work correctly.

## Information Gaps
- Exact TaskFlow version and auth library/framework in use (needed to locate the specific callback code)
- Whether TaskFlow uses the email claim, preferred_username, sub, or oid as the primary user-lookup key
- What the developer saw in browser dev tools regarding 'the cookie not sticking' — likely a symptom of the failed lookup but could reveal a secondary SameSite or Secure cookie-flag issue on the redirect
- Whether any affected users have neither plus-sign addresses nor email changes (would indicate a broader matching issue)
