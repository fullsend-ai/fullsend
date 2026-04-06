# Triage Summary

**Title:** OIDC login redirect loop after Okta-to-Entra ID migration for users with plus-addressed or mismatched emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during OIDC login. They authenticate successfully at Microsoft but are immediately redirected back to the login flow upon returning to TaskFlow. The issue is not browser-specific.

## Root Cause Hypothesis
TaskFlow's OIDC callback handler likely performs a user-lookup or session-binding step using the email claim from the ID token. Users with plus signs in their email addresses (e.g., jane+taskflow@company.com) may be failing a validation regex or normalization step, or the email returned by Entra ID differs from the email stored in TaskFlow's user record (due to alias remapping during migration). When the lookup fails, TaskFlow cannot establish a session and restarts the auth flow, causing the loop.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 (self-hosted) with OIDC SSO pointing to Microsoft Entra ID
  2. Create or use a user account whose email contains a plus sign (e.g., user+tag@company.com) or whose Entra primary email differs from the email stored in TaskFlow
  3. Attempt to log in via SSO
  4. Observe: authentication succeeds at Microsoft, but TaskFlow redirects back to Microsoft in a loop

## Environment
TaskFlow v2.3.1, self-hosted, OIDC authentication, Microsoft Entra ID as IdP (recently migrated from Okta)

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all. This is a complete access blocker for affected users with no known workaround.

## Recommended Fix
Investigate the OIDC callback handler's user-matching logic. Check (1) whether email claims containing '+' characters are being rejected, truncated, or incorrectly URL-decoded, and (2) whether the email claim from Entra ID tokens matches what TaskFlow has stored for those users. Likely fixes include normalizing email comparison (case-insensitive, encoding-aware), supporting lookup by sub claim or secondary/alias emails, or adding a mapping table for migrated accounts.

## Proposed Test Case
Write an integration test that performs an OIDC login flow with an ID token whose email claim contains a plus sign (user+tag@domain.com) and verify that the user is authenticated and a session is established without redirect. Add a second test where the ID token email differs from the stored user email but matches a known alias, and verify successful login.

## Information Gaps
- Exact session cookie configuration on the TaskFlow side
- Server-side logs from TaskFlow during a failed login attempt (would confirm whether the failure is at email matching, session creation, or token validation)
- Whether the plus-sign pattern vs. email-mismatch pattern are two separate issues or one
