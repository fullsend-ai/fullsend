# Triage Summary

**Title:** Login redirect loop caused by email mismatch between Entra ID UPN and TaskFlow stored email after SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. Entra ID authenticates them successfully, but TaskFlow fails to establish a session and redirects them back to the IdP. Affected users disproportionately have plus-sign email addresses (e.g., jane+taskflow@company.com) or had their email aliased during the Entra setup.

## Root Cause Hypothesis
Two overlapping issues: (1) **Primary — email mismatch:** TaskFlow performs user lookup by exact email match against the identity claim from the IdP callback. Entra ID sends the UPN (jane@company.com) which differs from the plus-addressed or aliased email stored in TaskFlow (jane+taskflow@company.com). The lookup fails, no session is created, and TaskFlow's auth middleware redirects back to the IdP, creating the loop. (2) **Secondary — stale Okta session cookies:** Some users still have cookies from the Okta-era auth flow that conflict with the new Entra flow, causing session establishment to fail even when the email would otherwise match. This explains why clearing cookies helps some users temporarily (resolves the cookie conflict alone) but not others (email mismatch persists regardless of cookie state), and why incognito windows sometimes work (no old cookies, and possibly a cache/timing factor in email resolution).

## Reproduction Steps
  1. Have a user whose TaskFlow profile email includes a plus-sign (e.g., jane+taskflow@company.com) while their Entra ID UPN is the base address (jane@company.com)
  2. User navigates to TaskFlow login page and clicks the SSO login button
  3. User is redirected to Microsoft Entra ID and authenticates successfully
  4. Entra ID redirects back to TaskFlow's callback URL with a valid token containing the UPN as the email claim
  5. TaskFlow attempts to look up a user matching the email from the token, finds no match (plus-sign address vs. plain address), fails to create a session
  6. TaskFlow's auth middleware sees no valid session and redirects back to the IdP, creating the loop

## Environment
Self-hosted TaskFlow instance. Recently migrated SSO from Okta to Microsoft Entra ID. Entra ID app registration and redirect URIs have been verified as correct. Affected users can log into other Entra-backed apps without issues.

## Severity: high

## Impact
Approximately 30% of the team cannot reliably log into TaskFlow. No dependable workaround exists — clearing cookies and incognito mode work intermittently. Affected users are locked out of the task management application.

## Recommended Fix
1. **Immediate fix:** Write a migration script to normalize emails in TaskFlow's user table to match Entra ID UPNs. For plus-addressed users, update stored emails to match their Entra UPN (or add the UPN as a recognized alias). 2. **Auth code fix:** In TaskFlow's SSO callback handler, find the user lookup logic (likely an exact-match query on email). Either normalize incoming email claims (strip plus-addressing) before lookup, or implement case-insensitive lookup with alias support. 3. **Cookie fix:** Invalidate all pre-migration session cookies — either by rotating the session signing key or by clearing the sessions table/store. This resolves the stale Okta cookie interference. 4. **Preventive:** Add logging to the auth callback path that records the claimed email and whether a user match was found, so future mismatches surface in logs rather than as silent redirect loops.

## Proposed Test Case
Create a test user in TaskFlow with email user+tag@example.com. Configure the test IdP to return user@example.com as the email claim. Attempt SSO login and verify that: (a) the user is matched correctly despite the plus-address difference, (b) a valid session is created, and (c) no redirect loop occurs. Additionally, test with a pre-existing session cookie from a different IdP configuration and verify it is handled gracefully (invalidated, not looped).

## Information Gaps
- Exact field name and matching logic in TaskFlow's auth callback code (exact match vs. case-insensitive, which OIDC claim is used — email vs. preferred_username vs. UPN)
- Whether TaskFlow supports email aliases or secondary email fields that could be leveraged instead of migrating primary emails
- The precise count and full list of affected users to confirm whether ALL affected users have an email discrepancy (reporter noted a couple cases were uncertain)
