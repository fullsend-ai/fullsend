# Triage Summary

**Title:** SSO redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Entra succeeds, but TaskFlow fails to complete the session, sending users back to the IdP. The affected population correlates strongly with users who have plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses changed/were aliased during the migration.

## Root Cause Hypothesis
TaskFlow's authentication callback matches users by comparing the email claim from the IdP token against stored user records. Entra ID is likely returning a different email value than Okta did for these users — either normalizing plus-addressed emails to the base address, returning the primary UPN instead of the alias, or using a different SAML/OIDC claim attribute for email. When the lookup fails, TaskFlow cannot establish a session, so it redirects back to the IdP, creating the loop. The intermittent cookie/incognito fix suggests a stale Okta-era session cookie may also interfere in some cases, but the root cause is the email mismatch on the server side.

## Reproduction Steps
  1. Create or identify a user in Entra ID with a plus-addressed email (e.g., testuser+taskflow@company.com)
  2. Ensure that user has an existing account in TaskFlow's database (linked via the plus-addressed email from the Okta era)
  3. Attempt to log into TaskFlow via SSO
  4. Observe the redirect loop: Entra authenticates successfully, TaskFlow callback fails to match the user, redirects back to Entra

## Environment
TaskFlow with SSO via Microsoft Entra ID (recently migrated from Okta). All users are cloud-only Entra accounts in the same security group. Reproduced across Chrome, Edge, and Firefox.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. No permanent workaround exists — clearing cookies and incognito mode are unreliable. These users are effectively locked out of the application.

## Recommended Fix
1. Inspect the OIDC/SAML token claims Entra ID sends during the callback (log the raw id_token or SAML assertion) and compare the email claim value against what TaskFlow has stored in its user table for affected users. 2. Check which claim attribute TaskFlow reads for email — Entra commonly uses `preferred_username`, `email`, or `upn`, and these may differ from what Okta provided. 3. Implement case-insensitive, plus-address-aware email matching in the auth callback (or normalize emails on both sides). 4. For migrated/aliased users, consider a one-time data migration to update stored emails to match what Entra returns, or add support for matching on multiple email aliases. 5. Invalidate stale Okta-era session cookies by rotating the session signing key or bumping the cookie name.

## Proposed Test Case
Write an integration test for the SSO callback handler that submits an id_token containing a plus-addressed email (user+tag@domain.com) and verifies that it correctly matches against a stored user record with that same plus-addressed email. Add a second case where the token contains the base email (user@domain.com) and verify it still matches users whose stored email is the plus-addressed variant.

## Information Gaps
- Exact claim attribute TaskFlow reads from the IdP token (e.g., email vs. upn vs. preferred_username)
- Whether TaskFlow's user table stores the original Okta-era email or has been updated post-migration
- Server-side auth callback logs showing the specific failure point in the redirect loop
