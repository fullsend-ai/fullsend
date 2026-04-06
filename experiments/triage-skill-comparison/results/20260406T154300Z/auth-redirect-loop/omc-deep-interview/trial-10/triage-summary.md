# Triage Summary

**Title:** SSO login redirect loop for users with plus-addressed or migrated emails after Okta-to-Entra ID switch

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users (~15 people) experience an infinite redirect loop when logging in. They authenticate successfully with Entra ID but are immediately redirected back to the login flow. The affected users share a common trait: they either use plus-addressing in their email (e.g., jane+taskflow@company.com) or had their email changed/aliased during the Okta-to-Entra migration. Clearing cookies or using incognito windows occasionally provides temporary relief but is inconsistent.

## Root Cause Hypothesis
TaskFlow's session establishment logic likely compares the email claim in the OIDC token returned by Entra ID against the user's stored email in TaskFlow's database. When these don't match — because Entra ID returns the canonical email while TaskFlow has the plus-addressed or old aliased variant (or vice versa) — the app fails to find/create a valid session and restarts the auth flow. The intermittent cookie-clearing fix suggests that stale session cookies from the Okta era may also interfere, causing the app to attempt to validate an old session token against the new IdP, fail, and redirect. The two factors (email mismatch preventing new session creation + stale cookies preventing clean auth attempts) compound to produce the loop.

## Reproduction Steps
  1. Set up a TaskFlow user account whose stored email uses plus-addressing (e.g., testuser+taskflow@company.com)
  2. Configure Entra ID so that the user's primary/canonical email is testuser@company.com (without the plus-address)
  3. Attempt to log into TaskFlow via SSO
  4. Observe that after successful Entra ID authentication, the user is redirected back to the login page in a loop
  5. Alternatively: take a user whose email was changed/aliased during the Okta-to-Entra migration and attempt login

## Environment
TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects multiple browsers. Entra ID app registration and redirect URIs verified correct. Affected users can authenticate to other Entra ID apps without issue.

## Severity: high

## Impact
~30% of the team (approximately 15 users) are blocked from logging into TaskFlow each morning. No reliable workaround exists. Daily productivity loss across the affected user group.

## Recommended Fix
1. Inspect the SSO callback handler where TaskFlow matches the incoming OIDC token's email claim to stored user records. Check whether the comparison is exact-match or normalized. 2. Log the exact email claim value Entra ID returns for affected users and compare it to what TaskFlow has stored — confirm the mismatch hypothesis. 3. Implement case-insensitive, plus-address-aware email normalization when matching identity claims to user records (strip the +tag portion before comparison, or match on a stable claim like `sub` or `oid` instead of email). 4. Add a migration script or one-time reconciliation to update stored emails for users whose addresses changed during the Okta-to-Entra migration. 5. Consider clearing or invalidating all pre-migration session cookies to eliminate stale Okta session interference.

## Proposed Test Case
Create integration tests for the SSO callback handler that verify: (a) a user with plus-addressed email in the database is matched when Entra ID returns the canonical email without the plus tag, (b) a user whose stored email differs from the Entra ID email claim by alias/domain is still matched via a stable identifier (oid/sub), (c) a user with a stale Okta session cookie is gracefully redirected through a clean auth flow rather than looping.

## Information Gaps
- Exact OIDC claims configuration in Entra ID (which claim is used as the email — UPN, preferred_username, or email)
- Whether TaskFlow matches users by email claim or by a stable OIDC subject identifier (sub/oid)
- Server-side logs from the SSO callback showing the specific failure point in the redirect loop
- Whether the app uses a session store (Redis, DB) or cookie-based sessions, which affects the stale session cleanup approach
