# Triage Summary

**Title:** Infinite login redirect loop caused by email claim mismatch after Okta-to-Entra-ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Entra ID, get redirected back to TaskFlow, but TaskFlow does not establish a session and immediately redirects them back to Entra ID. The affected population correlates with users who have plus-addressed emails (e.g. jane+taskflow@company.com) or whose email addresses were changed/aliased during the migration. Clearing cookies or using incognito sometimes breaks the loop, but not reliably.

## Root Cause Hypothesis
TaskFlow's OIDC callback handler matches the incoming identity token's email claim against stored user records. Entra ID likely returns a different claim value than Okta did for these users — common scenarios: (1) Entra ID returns the UPN (jane@company.com) instead of the primary email, stripping the plus-address tag; (2) Entra ID returns a new primary alias rather than the legacy email stored in TaskFlow's user table. When the lookup fails, TaskFlow never creates an authenticated session, so the auth middleware redirects back to the IdP, creating the loop. The secondary cookie/incognito factor suggests that stale Okta-era session or CSRF cookies may also interfere with the new auth flow, causing the loop even for some users whose email would otherwise match.

## Reproduction Steps
  1. Create or identify a test user in Entra ID with a plus-addressed email (e.g. testuser+taskflow@company.com)
  2. Ensure TaskFlow's user record stores that plus-addressed email
  3. Attempt to log in to TaskFlow via SSO
  4. Observe the redirect loop: Entra ID authenticates successfully, but TaskFlow redirects back to Entra ID repeatedly
  5. Inspect the OIDC id_token claims returned by Entra ID (especially 'email', 'preferred_username', and 'upn') and compare against the stored user email in TaskFlow's database
  6. Optionally reproduce the cookie variant: log in as a normal user, then swap to an affected user without clearing cookies

## Environment
TaskFlow with OIDC/SSO integration, recently migrated from Okta to Microsoft Entra ID. Affects roughly 30% of the user base. Affected users can authenticate to other Entra ID-connected apps without issue, confirming the problem is in TaskFlow's token handling, not Entra ID itself.

## Severity: high

## Impact
~30% of the team is unable to reliably log into TaskFlow, blocking their daily work. Workarounds (incognito, cookie clearing) are unreliable.

## Recommended Fix
1. **Inspect the token claims**: Log the full decoded id_token at TaskFlow's OIDC callback endpoint and compare the email-related claims (`email`, `preferred_username`, `upn`) for affected vs. unaffected users. Determine which claim Entra ID populates and whether it matches what TaskFlow looks up. 2. **Fix the user-matching logic**: Update the callback handler to normalize emails before lookup (strip plus-address tags, case-normalize, or match against known aliases). Consider matching on a stable immutable identifier (Entra ID `oid` or `sub` claim) rather than email. 3. **Clear stale session state**: On authentication callback, invalidate any pre-existing session cookies before establishing the new session to prevent stale Okta-era cookies from interfering. 4. **Backfill user records**: For users whose emails changed during migration, update or add email aliases in TaskFlow's user table so the lookup succeeds.

## Proposed Test Case
Unit/integration test for the OIDC callback user-matching function: given an id_token with email claim 'jane@company.com', verify it matches a stored user record with email 'jane+taskflow@company.com'. Additionally, verify that a user with a pre-existing session cookie from a different IdP configuration can still complete the login flow without looping.

## Information Gaps
- Exact OIDC claim(s) TaskFlow uses for user lookup (email vs. sub vs. upn) — requires checking the codebase
- Whether TaskFlow stores Okta-era subject identifiers that could be used as a migration bridge
- Whether the Entra ID app registration's optional claims configuration includes the 'email' claim or only returns UPN by default
