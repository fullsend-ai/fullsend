# Triage Summary

**Title:** SSO login redirect loop for users with '+' in email or changed email addresses after Okta-to-Entra migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, but TaskFlow immediately redirects them back to Microsoft again. The session is never established. Affected users can log into other Entra-ID-backed apps without issue.

## Root Cause Hypothesis
TaskFlow's user-matching logic compares the email claim from the Entra ID token against its stored user records to establish a session. This match is failing for two groups: (1) users with '+' in their email (e.g., jane+taskflow@company.com) — the '+' may be URL-encoded to '%2B' by Entra ID or during the redirect, causing a mismatch; (2) users whose primary email was changed/aliased during the migration — the email in the Entra ID token doesn't match the original email stored in TaskFlow's user table. When the match fails, TaskFlow treats the user as unauthenticated and restarts the login flow, creating the loop.

## Reproduction Steps
  1. Set up TaskFlow with Entra ID SSO integration
  2. Create or use a test user account with a '+' character in the email address (e.g., testuser+taskflow@company.com)
  3. Attempt to log into TaskFlow via SSO
  4. Observe: authentication succeeds at Microsoft but TaskFlow redirects back to Microsoft in an infinite loop
  5. Alternatively: create a user with one email, then change their primary email in Entra ID to a different address, and attempt login

## Environment
TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Entra ID app registration and redirect URIs verified as correct. Affects ~30% of users — those with '+' in emails or changed/aliased email addresses.

## Severity: high

## Impact
~30% of users are completely locked out of TaskFlow with no workaround. This is a blocker for those users since the SSO migration has already been completed.

## Recommended Fix
Investigate TaskFlow's SSO callback handler — specifically how it extracts the user identity from the Entra ID token and matches it to local user records. (1) Ensure the email comparison handles URL-encoding (decode '%2B' back to '+' before matching). (2) Ensure the comparison is case-insensitive. (3) For users with changed emails, consider matching on a stable identifier (such as the OIDC 'sub' claim or Entra Object ID) rather than email alone, or add support for matching against email aliases/previous emails. As a quick mitigation, a lookup against both the current and previous email fields (if stored) would unblock aliased users.

## Proposed Test Case
Create integration tests for the SSO callback handler that verify: (a) a user with '+' in their email (e.g., 'user+tag@domain.com') is correctly matched and logged in; (b) a user whose email was changed from 'old@domain.com' to 'new@domain.com' can log in when the token contains 'new@domain.com'; (c) URL-encoded email values in the callback are properly decoded before user lookup; (d) no redirect loop occurs when the user match succeeds.

## Information Gaps
- Exact SSO protocol in use (SAML 2.0 vs. OIDC) — affects where the email claim is extracted from
- Which claim/attribute TaskFlow uses for user matching (email, nameID, sub, UPN)
- Whether TaskFlow stores the original Okta subject identifiers and whether those could be leveraged
- Server-side logs from the redirect loop showing the exact point of failure
