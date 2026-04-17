# Triage Summary

**Title:** Login redirect loop for users with plus-addressed emails after Okta-to-Entra ID SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. These users authenticate successfully with Entra ID but are never granted a TaskFlow session, causing the app to repeatedly redirect them back to the IdP. The affected users are those whose TaskFlow accounts were registered with plus-addressed emails (e.g., jane+taskflow@company.com).

## Root Cause Hypothesis
TaskFlow's authentication callback performs an exact-match email lookup to find or create a user session. Entra ID's token returns the user's primary/canonical email (jane@company.com) without the plus-address suffix, while TaskFlow has the plus-addressed form (jane+taskflow@company.com) stored in its user database. The lookup fails, no session is established, and the user is redirected back to login — creating the loop. Okta likely preserved the plus-addressed form in its claims, which is why this only surfaced after the migration.

## Reproduction Steps
  1. Have a user account in TaskFlow registered with a plus-addressed email (e.g., jane+taskflow@company.com)
  2. Configure the corresponding Entra ID user with primary email jane@company.com
  3. Attempt to log into TaskFlow via SSO
  4. Observe: user authenticates successfully at Entra ID, is redirected to TaskFlow callback, but is immediately redirected back to Entra ID in a loop

## Environment
TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects Chrome and Edge. Not browser-specific.

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all. These are users who set up plus-addressed emails for filtering purposes. Complete login blockage for affected users with no reliable workaround.

## Recommended Fix
1. Identify where TaskFlow matches the incoming SSO email claim against stored user emails (the auth callback/session creation code). 2. Normalize the comparison: strip the plus-address suffix (everything between + and @) before lookup, or implement case-insensitive matching on the base address. 3. Alternatively, configure the Entra ID app registration to emit a claim that includes the full plus-addressed email (e.g., using a claims mapping policy). 4. Also check which specific claim TaskFlow reads (email, preferred_username, upn) — Entra ID populates these differently and the plus-address may be available in one but not others. 5. Address the stale-cookie issue by ensuring failed auth attempts don't leave partial session state that blocks subsequent attempts.

## Proposed Test Case
Create a test user with plus-addressed email (test+sub@example.com) in TaskFlow's user store. Mock an SSO callback with the canonical email (test@example.com) in the token claims. Verify that the user is matched correctly, a session is created, and no redirect loop occurs.

## Information Gaps
- Exact token claim TaskFlow reads for email matching (email vs. preferred_username vs. upn) — developer can inspect the auth code and token
- Whether users with changed/aliased emails (non-plus-addressing) are also affected — same fix would apply
- Whether Okta was configured to pass through plus-addressed emails or if TaskFlow's old auth code handled normalization
