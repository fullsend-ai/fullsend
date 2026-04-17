# Triage Summary

**Title:** Login redirect loop caused by email mismatch between Entra ID claims and TaskFlow user records after SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop during login. The authentication with Entra succeeds, but TaskFlow cannot match the authenticated user to an existing account because the email claim returned by Entra ID differs from the email stored in TaskFlow's user database.

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches users by comparing the email claim from the identity provider against stored user emails. When no match is found, instead of returning an error, the app silently restarts the auth flow, causing an infinite redirect loop. Two categories of mismatch have been confirmed: (1) users with plus-addressed emails in TaskFlow (e.g., jane+taskflow@company.com) where Entra returns the base address (jane@company.com), and (2) users whose email aliases changed during the Entra migration (e.g., mjones@company.com in TaskFlow vs. mike.jones@company.com in Entra). A secondary issue exists where stale session cookies persist after the email is corrected, requiring a cookie clear before the fix takes effect.

## Reproduction Steps
  1. Identify a user whose email in TaskFlow differs from their primary email or UPN in Entra ID (e.g., plus-addressed email or old alias)
  2. Have that user attempt to log into TaskFlow via SSO
  3. User authenticates successfully with Entra ID
  4. User is redirected back to TaskFlow, which fails to match the Entra email claim to any account
  5. TaskFlow restarts the auth flow, redirecting back to Entra, creating an infinite loop

## Environment
TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox equally. Not browser- or device-specific.

## Severity: high

## Impact
Approximately 30% of users are completely locked out of TaskFlow. These are existing users with legitimate accounts whose emails don't match the new identity provider's claims.

## Recommended Fix
1. **Immediate (data fix):** Audit all TaskFlow user emails against Entra ID primary emails/UPNs and reconcile mismatches. Advise affected users to clear TaskFlow cookies after the fix. 2. **Short-term (auth handler):** Fix the SSO callback to return a clear error message (e.g., 'No account found for this email — contact your admin') instead of silently restarting the auth flow, which prevents the redirect loop for any future mismatches. 3. **Medium-term (robustness):** Consider matching users on a stable, provider-independent identifier (such as the OIDC `sub` claim or an internal user ID) rather than email alone, or implement case-insensitive email normalization that strips plus-addressing and resolves known aliases.

## Proposed Test Case
Create a test user in TaskFlow with email 'testuser+tag@company.com'. Configure the SSO mock/stub to return 'testuser@company.com' as the email claim. Attempt login and verify: (a) the user is either matched correctly or shown a clear error — not a redirect loop, and (b) after email reconciliation, the user can log in successfully.

## Information Gaps
- Exact field Entra ID returns as the email claim (email, preferred_username, or UPN) — may affect normalization strategy
- Whether TaskFlow's user lookup is case-sensitive, which could cause additional mismatches beyond the confirmed ones
- Whether the cookie/session persistence issue requires a code fix (e.g., clearing session on auth failure) or is just a one-time browser cache concern
