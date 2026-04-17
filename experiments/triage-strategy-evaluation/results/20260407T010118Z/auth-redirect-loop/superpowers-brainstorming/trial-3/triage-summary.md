# Triage Summary

**Title:** Login redirect loop after Okta→Entra ID SSO migration due to email mismatch and stale session state

## Problem
After switching SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, and are immediately sent back to Microsoft. Clearing cookies/incognito provides inconsistent temporary relief. Affected users can log into other Entra-backed apps without issue.

## Root Cause Hypothesis
Two overlapping issues: (1) TaskFlow's user-matching logic compares the email claim from the Entra ID token against its stored user records using an exact string match. Users with plus-addressed emails (e.g., jane+taskflow@company.com) or changed/aliased emails have a mismatch because Entra sends the canonical email (jane@company.com). Authentication succeeds at the IdP but TaskFlow can't resolve the user, triggering a redirect back to login. (2) Stale OAuth state cookies, OIDC nonce cookies, or session tokens from the old Okta integration persist in browsers and interfere with the new Entra auth flow, causing transient failures even for users without email mismatches and explaining why clearing cookies sometimes helps temporarily but the loop can recur as new broken session state is written.

## Reproduction Steps
  1. Have a user whose TaskFlow account email uses plus-addressing (e.g., jane+taskflow@company.com) or was changed/aliased after initial signup
  2. Attempt to log into TaskFlow via the Entra ID SSO flow
  3. Observe the user authenticates successfully at Microsoft but gets caught in an infinite redirect loop back to TaskFlow's login page
  4. Clear all cookies and retry — loop may temporarily resolve but recurs in subsequent sessions

## Environment
TaskFlow with SSO integration, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected (Chrome, Edge, Firefox). Entra ID app registration and redirect URIs verified correct by the reporter.

## Severity: high

## Impact
Approximately 30% of the team is locked out of TaskFlow entirely. The issue is persistent and not reliably resolved by user-side workarounds (clearing cookies). Productivity impact across the affected user base.

## Recommended Fix
Investigate two areas: (1) **Email normalization in user matching** — Check how TaskFlow resolves the authenticated user after receiving the OIDC token from Entra. The matching logic should normalize emails by stripping plus-address suffixes and checking against known aliases, not just doing an exact string comparison. Compare the `email` and `preferred_username` claims in Entra sign-in logs against TaskFlow's user database for affected users to confirm the mismatch. (2) **Stale session cleanup** — Identify and clear any Okta-era cookies (OAuth state, OIDC nonce, session tokens) that TaskFlow's auth flow may still be reading. Consider adding a one-time migration step that invalidates all pre-migration sessions, or explicitly scoping new Entra session cookies so they don't collide with old Okta ones. Check server-side logs during a redirect loop to see whether TaskFlow is throwing a 'user not found' error, a session validation error, or something else — this will clarify which of the two issues is primary.

## Proposed Test Case
Create test users with plus-addressed emails (user+tag@domain.com) and aliased emails, configure Entra to return the canonical (non-plus) email in the token, and verify that TaskFlow correctly resolves these users without entering a redirect loop. Additionally, simulate stale Okta session cookies in the browser and verify that the Entra auth flow completes successfully without interference.

## Information Gaps
- Server-side logs from TaskFlow during a redirect loop — would reveal the exact failure point (user lookup failure vs session error vs something else)
- Exact token claims from Entra sign-in logs compared to TaskFlow user records for affected users — reporter plans to check but hasn't yet
- Whether the 'UI looks different' observation indicates other changes were made to TaskFlow during the SSO migration
- Whether TaskFlow stores the original email from initial signup or updates it — relevant to the alias mismatch theory
