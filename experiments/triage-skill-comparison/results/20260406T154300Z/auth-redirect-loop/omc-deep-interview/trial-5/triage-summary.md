# Triage Summary

**Title:** SSO redirect loop caused by email mismatch between Entra ID claims and stored user identities (plus-addressing and renamed accounts)

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Entra ID succeeds, but TaskFlow fails to establish a session and redirects the user back to the IdP. Affected users are those whose email in Entra ID doesn't exactly match what TaskFlow has stored from the Okta era — specifically plus-addressed emails (e.g., jane+taskflow@company.com) and users whose email changed (e.g., name change).

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches the incoming identity token's email claim against its user database using exact string comparison. When the email in the Entra ID token differs from the email stored in TaskFlow (due to plus-address normalization differences or email changes during migration), the lookup fails. Without a matching user record, TaskFlow cannot create an authenticated session, so it falls back to the login flow, creating the loop. Stale session cookies from the Okta era may compound the issue by presenting conflicting identity state, which explains the intermittent success after clearing cookies or using incognito mode.

## Reproduction Steps
  1. 1. Identify a user account that uses a plus-addressed email (e.g., user+taskflow@company.com) or one whose email was changed between Okta and Entra ID
  2. 2. Attempt to log into TaskFlow using SSO
  3. 3. Authenticate successfully with Microsoft Entra ID
  4. 4. Observe the redirect back to TaskFlow, which immediately redirects back to Entra ID in a loop
  5. 5. Optionally: compare the email claim in the Entra ID token (decode the JWT or check IdP logs) against the email stored in TaskFlow's user table for that account

## Environment
TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects users with plus-addressed emails and users whose email changed during migration. Other Entra ID apps work fine for the same users, confirming the issue is in TaskFlow's handling, not in Entra ID configuration.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. No consistent workaround exists (incognito/cookie clearing is intermittent). Blocks daily task management for affected users.

## Recommended Fix
1. Inspect TaskFlow's SSO callback handler — find where it matches the incoming email claim against the user database. 2. Normalize email comparison: strip plus-address suffixes and do case-insensitive matching, or match on a stable identifier (e.g., Entra ID object ID / sub claim) rather than email. 3. Add a migration step or admin tool to reconcile user records: map old Okta emails to current Entra ID emails for users with changed addresses. 4. Ensure the session creation failure path returns an informative error rather than silently redirecting back to the IdP. 5. Consider clearing or invalidating pre-migration session cookies on first post-migration login attempt.

## Proposed Test Case
Create test users with plus-addressed emails and with email mismatches (stored email differs from IdP email claim). Verify that SSO login succeeds for these users and a valid session is established. Also verify that stale session cookies from a previous IdP configuration do not cause redirect loops.

## Information Gaps
- Exact email claim field Entra ID is sending (email vs preferred_username vs UPN) — may affect normalization logic
- Whether TaskFlow uses SAML or OIDC for SSO — affects where to look for the matching logic
- Database schema for user identity storage — whether there's a separate SSO identifier column or only email
