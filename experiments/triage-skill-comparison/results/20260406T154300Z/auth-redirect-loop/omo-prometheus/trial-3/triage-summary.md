# Triage Summary

**Title:** Login redirect loop for users with '+' in email or aliased emails after Okta-to-Entra ID SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but are immediately redirected back to the identity provider upon returning to TaskFlow. The affected population correlates strongly with users who have '+' characters in their email addresses (plus addressing) or whose email addresses were changed/aliased after initial TaskFlow signup.

## Root Cause Hypothesis
TaskFlow's authentication callback is failing to match the identity returned by Entra ID to existing user records, likely due to one or both of these issues: (1) The '+' character in email addresses is being URL-encoded or decoded incorrectly during the OAuth2/OIDC callback ('+' becomes a space in application/x-www-form-urlencoded contexts), causing the email claim to not match the stored user record. (2) For aliased users, the email claim Entra ID returns differs from what Okta previously returned (e.g., Entra sends the primary alias while TaskFlow stored the original signup email). When the match fails, TaskFlow likely does not establish a valid session, so the auth middleware redirects back to the IdP, creating the loop. The inconsistent success with cookie-clearing and incognito windows suggests that occasionally the token round-trip preserves the email correctly (perhaps via a different encoding path or cached token), but the default flow corrupts it.

## Reproduction Steps
  1. Configure TaskFlow SSO with Microsoft Entra ID
  2. Create a user account with a plus-addressed email (e.g., jane+taskflow@company.com)
  3. Attempt to log in via SSO
  4. Observe: successful Entra ID authentication followed by redirect back to TaskFlow, which immediately redirects back to Entra ID in a loop
  5. Compare: repeat with a user whose email has no special characters — login succeeds

## Environment
TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Users affected have '+' in email addresses or email aliases/changes. Other Entra ID-integrated apps work fine for these same users.

## Severity: high

## Impact
Approximately 30% of the team is effectively locked out of TaskFlow. Workaround (incognito/cookie clearing) is unreliable. Productivity impact is significant as affected users cannot consistently access the task management application.

## Recommended Fix
Investigate TaskFlow's OAuth2/OIDC callback handler, specifically: (1) How the email claim from the ID token or userinfo endpoint is parsed — check for incorrect URL decoding that converts '+' to space. (2) How the parsed email is matched against stored user records — the match query should normalize or be case-insensitive and encoding-aware. (3) Whether TaskFlow stores the original signup email from Okta and compares it against what Entra ID returns — if Entra sends a different alias, the lookup fails. Fix should include: properly handling RFC 5322 email characters in the auth callback, and matching users by a stable identifier (like the OIDC 'sub' claim or an internal user ID) rather than relying solely on email string comparison.

## Proposed Test Case
Create integration tests for the SSO callback that (1) send an ID token with a plus-addressed email and verify the user session is created correctly, (2) send an ID token where the email claim differs from the stored user email (alias scenario) and verify matching falls back to a secondary identifier, and (3) verify no redirect loop occurs when the email contains URL-sensitive characters (+, %, etc.).

## Information Gaps
- Whether TaskFlow uses OIDC or SAML with Entra ID (affects where the email encoding issue occurs)
- The exact claim TaskFlow uses for user matching (email vs. sub vs. preferred_username)
- Server-side logs from TaskFlow during a failed login attempt — would confirm the matching failure
- Whether the Okta integration used a different claim or email format that masked this bug
