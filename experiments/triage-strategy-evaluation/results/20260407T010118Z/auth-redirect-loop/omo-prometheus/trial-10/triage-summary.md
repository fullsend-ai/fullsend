# Triage Summary

**Title:** Login redirect loop caused by email claim mismatch between Entra ID and TaskFlow user records (plus-addressing / aliased emails)

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully with Microsoft, get redirected back to TaskFlow, but TaskFlow fails to match the returned identity to a stored user and redirects them back to Microsoft. The loop repeats indefinitely.

## Root Cause Hypothesis
TaskFlow's session establishment logic matches the email claim from the SSO ID token against its stored user records. Entra ID normalizes plus-addressed emails (e.g., jane+taskflow@company.com → jane@company.com) and may return different email values for users whose addresses were aliased during migration. When the returned email doesn't exactly match TaskFlow's stored email, the user lookup fails, no session is created, and the auth middleware redirects back to the IdP — creating the loop. The cookie not persisting is a secondary symptom: because the redirect fires immediately, the session cookie set on the return from Microsoft is discarded before it can be established.

## Reproduction Steps
  1. Ensure a user exists in TaskFlow with a plus-addressed email (e.g., jane+taskflow@company.com)
  2. Configure Entra ID as the SSO provider with the same user's base email (jane@company.com)
  3. Attempt to log in as that user
  4. Observe: successful Entra ID authentication → redirect to TaskFlow → immediate redirect back to Entra ID → infinite loop

## Environment
TaskFlow with Microsoft Entra ID SSO (migrated from Okta). Affects users with plus-sign email addresses and users whose email addresses were changed or aliased during the Entra ID migration.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. Workarounds (incognito, clearing cookies) are inconsistent and unreliable. This is a complete access blocker for affected users with no dependable workaround.

## Recommended Fix
1. Identify where TaskFlow matches the incoming SSO email claim to stored user records (likely in the OAuth callback handler or session middleware). 2. Implement email normalization before comparison — at minimum, strip plus-addressing (everything between + and @) before lookup. 3. Consider also matching on the `sub` (subject) claim or `oid` (object ID) claim from Entra ID as a stable identifier that survives email changes. 4. For immediate relief: update the affected users' email addresses in TaskFlow's database to match what Entra ID actually returns, or add a secondary email/alias lookup. 5. Review the Set-Cookie attributes (SameSite, Secure, Domain, Path) on the session cookie to ensure they're compatible with the redirect flow — though this is likely a symptom, not the cause.

## Proposed Test Case
Create a test user in TaskFlow with email user+tag@example.com. Configure SSO to return user@example.com as the email claim. Verify that login succeeds and a session is established. Also test with an aliased email (stored: old@example.com, SSO returns: new@example.com) to cover the migration alias case.

## Information Gaps
- Exact TaskFlow code path for SSO user matching (email field vs. sub/oid claim)
- Whether TaskFlow stores any secondary identifiers (e.g., Okta subject IDs) that could be remapped to Entra ID identifiers
- Exact Set-Cookie attributes on the session cookie (SameSite, Secure flags)
- Whether the few non-plus-addressed affected users had email aliases changed during Entra ID migration (likely, but not explicitly confirmed for every case)
