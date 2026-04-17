# Triage Summary

**Title:** SSO login redirect loop for users with plus-addressed emails after Okta→Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. The user authenticates successfully with Entra ID but is immediately redirected back to login. The common factor among affected users is that their email stored in TaskFlow uses plus-addressing (e.g., jane+taskflow@company.com), while Entra ID returns the base email (jane@company.com) as the UPN.

## Root Cause Hypothesis
TaskFlow's SSO callback matches the authenticated user by comparing the email claim from the identity provider against the email stored in its user database. Okta preserved plus-addressed emails in its claims, but Entra ID returns the UPN (base email without the plus-address suffix). When TaskFlow receives jane@company.com but only has jane+taskflow@company.com on file, the lookup fails. Without a matched user, no session is created. The next request sees no session, triggers SSO login, which auto-succeeds (Entra session is active), and the cycle repeats — producing the redirect loop instead of an explicit error.

## Reproduction Steps
  1. Ensure a user exists in TaskFlow with a plus-addressed email (e.g., jane+taskflow@company.com)
  2. Ensure the same user's Entra ID UPN is the base address (jane@company.com)
  3. Attempt to log in to TaskFlow via SSO
  4. Observe: authentication succeeds at Entra, redirect back to TaskFlow, immediate redirect back to Entra, looping indefinitely

## Environment
TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Affects users whose TaskFlow-stored email differs from their Entra ID UPN, primarily those using plus-addressed emails.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. No workaround is consistently effective. This blocks daily work for affected users.

## Recommended Fix
1. In TaskFlow's SSO auth callback, locate the email-matching logic that maps the IdP-returned identity to a local user. 2. Implement normalized email comparison — strip plus-address suffixes before matching, or match on the base email portion. 3. Alternatively, add a secondary lookup by alias/UPN if the primary email match fails. 4. Consider adding a user-facing error page when no matching account is found (instead of silently looping) to prevent redirect loops for any future identity mismatches. 5. For immediate relief, affected users' emails in TaskFlow's database could be updated to match their Entra ID UPN.

## Proposed Test Case
Create a test user in TaskFlow with email user+tag@domain.com. Configure SSO to return user@domain.com as the email claim. Verify that login succeeds and maps to the correct user. Also verify that when no user matches at all, the system shows an error page rather than looping.

## Information Gaps
- Not 100% confirmed that all affected users have plus-addressed or aliased emails (reporter suspects additional alias-based mismatches from the Entra migration but hasn't verified all affected users)
- Exact code path of TaskFlow's auth callback and session establishment logic is unknown — the redirect-loop-instead-of-error behavior should be confirmed in code
