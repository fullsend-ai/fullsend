# Triage Summary

**Title:** SSO redirect loop after Okta-to-Entra ID migration: session cookie not persisting for subset of users

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Microsoft but the session cookie set during the callback does not persist, causing TaskFlow to treat them as unauthenticated and redirect them back to Microsoft repeatedly.

## Root Cause Hypothesis
The most likely cause is a user-identity mismatch between the Okta-era user records in TaskFlow's database and the claims/attributes returned by Entra ID. Users whose email changed during the migration, or who use plus-addressed emails (e.g., jane+taskflow@company.com), may fail a user-lookup or token-validation step during the SSO callback, preventing a valid session from being created. The server likely sets the cookie optimistically but then fails to associate it with a user, resulting in the redirect. The intermittent success in incognito windows suggests stale cookies or cached auth state from the old Okta flow may also be interfering.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 with Entra ID SSO configured
  2. Attempt to log in as a user whose email was changed during the Okta-to-Entra migration, or who uses a plus-addressed email
  3. Complete authentication on the Microsoft side
  4. Observe redirect back to TaskFlow followed by immediate redirect back to Microsoft in a loop
  5. Verify in browser dev tools that a Set-Cookie header is present in the callback response but the cookie does not persist on subsequent requests

## Environment
TaskFlow v2.3.1, self-hosted, Docker on Ubuntu Linux, SSO via Microsoft Entra ID (recently migrated from Okta)

## Severity: high

## Impact
Approximately 30% of the team is completely unable to log into TaskFlow. This is a login blocker with no reliable workaround (incognito works inconsistently).

## Recommended Fix
Investigate the SSO callback handler in TaskFlow's auth module: (1) Check how the user lookup works during the OIDC/SAML callback — does it match on email claim, subject/nameID, or an internal ID? If it matches on email, plus-addressing normalization or email-change mismatches could cause lookup failures. (2) Check whether the session is actually created and persisted to the session store, or if an error during user lookup silently prevents session creation while still setting the cookie. (3) Check the SameSite and Secure attributes on the session cookie — the Entra ID redirect flow may differ from Okta's in a way that triggers browser cookie-rejection policies. (4) Consider adding a migration step or user-matching fallback that reconciles old Okta identities with new Entra ID claims.

## Proposed Test Case
Create test users with plus-addressed emails and with emails that differ between the identity provider claim and the TaskFlow user record. Execute the SSO login flow and verify that (a) the user lookup succeeds and maps to the correct account, (b) a valid session is created and persisted, and (c) the session cookie is present and accepted on the post-redirect request to TaskFlow.

## Information Gaps
- Server-side logs from TaskFlow during a failed login attempt (reporter will provide once DevOps colleague is available)
- Exact deployment details (Docker configuration, reverse proxy setup) — a reverse proxy stripping or misconfiguring cookie headers could also cause this
- Confirmation of whether all affected users have email mismatches between old Okta records and current Entra ID claims
