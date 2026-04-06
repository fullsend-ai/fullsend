# Triage Summary

**Title:** SSO login redirect loop for users with plus-addressed or migrated email addresses after Okta-to-Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. Users authenticate successfully with Microsoft but are immediately redirected back to the IdP upon returning to TaskFlow. The affected population correlates with users who use plus addressing (e.g., jane+taskflow@company.com) or whose email address changed between the Okta and Entra ID configurations.

## Root Cause Hypothesis
TaskFlow is likely matching the authenticated user by comparing the email claim from the OIDC/SAML token against stored user records using an exact string match. When Entra ID returns 'jane@company.com' but the TaskFlow account was created under 'jane+taskflow@company.com' (or vice versa), or when the stored email reflects the old Okta identity, the lookup fails. Without a matching session/user record, TaskFlow treats the user as unauthenticated and initiates a new login flow, causing the loop. The intermittent success in incognito windows may be due to cached cookies or stale session tokens from the old Okta integration interfering in normal browser sessions.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO
  2. Create or have a user account whose email in TaskFlow uses plus addressing (e.g., user+taskflow@company.com) or differs from their Entra ID primary email
  3. Attempt to log in via SSO
  4. Observe that authentication succeeds at Microsoft but TaskFlow redirects back to Microsoft in a loop

## Environment
TaskFlow v2.3.1, self-hosted. Multiple browsers affected (Chrome, Edge, Firefox). SSO provider: Microsoft Entra ID (migrated from Okta).

## Severity: high

## Impact
~30% of the organization is locked out of TaskFlow entirely. These users cannot work in the application. Workarounds (incognito windows) are unreliable.

## Recommended Fix
Investigate the user lookup logic in the SSO callback handler. Check how TaskFlow resolves the identity claim (email/sub/UPN) from the Entra ID token to a local user record. Likely fixes: (1) Normalize email addresses before comparison — strip plus-address tags and compare case-insensitively. (2) Add a migration step or admin tool to update stored user emails to match Entra ID primary identities. (3) Consider matching on a stable claim like the OIDC 'sub' rather than email. Also investigate whether stale Okta session cookies are contributing to the loop and add logic to clear legacy SSO cookies on the new login flow.

## Proposed Test Case
Create test users with plus-addressed emails and with emails that differ from their IdP primary email. Verify that SSO login completes successfully and resolves to the correct TaskFlow account without looping. Also test that a user with a stale session cookie from a previous IdP configuration can log in cleanly.

## Information Gaps
- No server-side logs or browser network traces to confirm the exact point of failure in the redirect loop
- Unknown whether TaskFlow matches on email, UPN, or OIDC sub claim
- Unknown whether clearing browser cookies/cache reliably resolves the issue for affected users
