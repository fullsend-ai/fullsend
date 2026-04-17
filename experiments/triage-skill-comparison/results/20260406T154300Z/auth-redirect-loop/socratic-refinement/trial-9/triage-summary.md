# Triage Summary

**Title:** Login redirect loop caused by email claim mismatch after Okta-to-Entra-ID SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Entra ID succeeds, but TaskFlow fails to establish a session, sending the user back to the IdP. The affected users predominantly have plus-addressed emails (e.g., jane+taskflow@company.com) or have had email address changes/aliases applied in the directory.

## Root Cause Hypothesis
TaskFlow matches the incoming SSO identity to its local user records using an email claim from the IdP token. Entra ID formats or normalizes this email claim differently than Okta did — particularly for plus-addressed emails and aliased accounts. When the email in the Entra ID token doesn't exactly match what TaskFlow has stored (from the Okta era), the user lookup fails silently. Without a matched user, TaskFlow cannot create a session, so the auth middleware treats the user as unauthenticated and redirects them back to the IdP, creating the loop. The few affected users without '+' addresses are likely those whose email changed — their Entra ID token carries the new/primary email while TaskFlow still has the old one.

## Reproduction Steps
  1. Create or identify a TaskFlow user account whose stored email contains a '+' character (e.g., jane+taskflow@company.com)
  2. Configure TaskFlow SSO to use Microsoft Entra ID
  3. Attempt to log in as that user via SSO
  4. Observe that after successful Entra ID authentication, the browser enters a redirect loop between TaskFlow and the IdP
  5. Compare the email claim in the Entra ID token (decode the JWT or check IdP logs) with the email stored in TaskFlow's user table for that user

## Environment
TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers (Chrome, Edge, Firefox). Server-side issue, not client-specific.

## Severity: high

## Impact
~30% of the team is completely locked out of TaskFlow with no workaround. This blocks their work and will affect more users over time as email changes accumulate.

## Recommended Fix
1. Inspect the SSO callback handler to identify which claim TaskFlow uses for user matching (likely `email` or `preferred_username`). 2. Compare the claim value from Entra ID tokens against stored user records for affected users — confirm the mismatch. 3. Fix the matching logic: either normalize emails before comparison (e.g., strip plus-addressing suffixes if Entra ID does so), or switch to a stable, immutable identifier like the `sub` (subject) claim or `oid` (object ID) for user matching instead of email. 4. For the aliased-email users, either update TaskFlow's stored emails to match Entra ID, or implement a migration script that maps old emails to new ones. 5. As a short-term fix, consider adding a fallback lookup that tries matching on a secondary identifier when the primary email lookup fails.

## Proposed Test Case
Unit test for the user-matching function: given a stored user with email 'jane+taskflow@company.com' and an incoming SSO token with email claim 'jane+taskflow@company.com' (exact match), 'jane@company.com' (stripped plus address), or 'JANE+TASKFLOW@COMPANY.COM' (case variation), verify that all variants resolve to the correct user and a session is created. Additionally, test that a user whose stored email is 'old@company.com' can be matched when the token carries 'new@company.com' if a mapping exists.

## Information Gaps
- Exact claim(s) TaskFlow uses for user identity matching (email, preferred_username, sub, oid, etc.)
- Whether Entra ID is actively stripping or transforming the '+' in plus-addressed emails, or if the mismatch is due to a different claim being sent entirely
- TaskFlow's SSO callback handler code — whether it logs match failures or silently redirects
- Whether the non-plus-addressed affected users had their Okta accounts linked by a different identifier than email
