# Triage Summary

**Title:** Infinite login redirect loop after Okta-to-Entra-ID SSO migration for users with plus-sign or remapped email addresses

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Entra ID, get redirected back to TaskFlow, and are immediately redirected back to Entra ID. Clearing cookies temporarily fixes the issue, but the loop recurs on subsequent visits or after idle time, indicating the login flow itself is re-creating the bad state.

## Root Cause Hypothesis
TaskFlow's authentication callback is likely failing to match the identity from Entra ID's OIDC token to the existing user record, due to email normalization issues (plus-sign addresses like jane+taskflow@company.com being treated differently, or email addresses that changed during the migration). When the match fails, TaskFlow probably writes a session cookie indicating an unauthenticated or invalid state, then redirects back to the SSO provider, creating the loop. The incognito-window success and temporary cookie-clearing fix confirm that stale/conflicting session state is involved, but the loop's return proves the app is actively re-creating the bad state on each auth callback — not just carrying it over from the Okta era.

## Reproduction Steps
  1. 1. Identify a user with a plus-sign email address (e.g., jane+taskflow@company.com) or one whose primary email was remapped during the Okta-to-Entra migration
  2. 2. Have the user clear all cookies for the TaskFlow domain and login.microsoftonline.com
  3. 3. Navigate to TaskFlow and click Login
  4. 4. Complete authentication at Entra ID (authentication succeeds)
  5. 5. Observe redirect back to TaskFlow, then immediate redirect back to Entra ID in a loop
  6. 6. Alternatively, log in successfully (may work once after clearing cookies), close the browser, reopen, and navigate to TaskFlow — loop should recur

## Environment
TaskFlow web application with OIDC/SSO authentication. Recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox equally. Some users have plus-sign email addresses for notification filtering. Some users had primary emails remapped during the Entra ID migration.

## Severity: high

## Impact
Approximately 30% of the team (~7-9 users) cannot reliably log into TaskFlow. There is no durable workaround — clearing cookies provides only temporary relief. This blocks daily work for affected users.

## Recommended Fix
1. Inspect TaskFlow's OIDC callback handler: examine how it matches the incoming identity token (email/sub claim) to existing user records. Check whether email comparison is exact-match or if it normalizes plus-sign aliases and case. 2. Compare the email/sub claims in Entra ID tokens for affected vs. unaffected users against what TaskFlow has stored in its user table. 3. Check the session/cookie logic in the callback: when the user match fails, does it set a cookie that triggers re-authentication rather than showing an error? This silent-failure-to-redirect pattern is the likely loop mechanism. 4. Fix the email matching to handle plus-sign normalization (strip the +tag portion before comparison) and account for email address changes from the migration. 5. Consider adding a loop-detection mechanism (e.g., count redirects within a time window) that surfaces an error message instead of looping indefinitely.

## Proposed Test Case
Create test users with these email variants: (a) standard email matching Entra and TaskFlow exactly, (b) plus-sign email like user+tag@domain.com, (c) email that differs between Entra primary email and TaskFlow stored email. Verify that the OIDC callback successfully matches and authenticates all three variants without redirect loops. Additionally, test that after a session expires or browser restart, re-authentication succeeds without requiring cookie clearing.

## Information Gaps
- Exact email comparison logic in TaskFlow's OIDC callback handler (requires code inspection)
- Precise contents of the OIDC token claims (email, sub, preferred_username) for affected vs. unaffected users
- Whether TaskFlow uses a session cookie, JWT in localStorage, or another mechanism for post-auth session persistence
- Full audit of which affected users have plus-sign emails vs. remapped emails vs. both — reporter estimated from memory
- Whether the 1-2 plus-sign users who are NOT affected have any distinguishing configuration
