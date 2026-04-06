# Triage Summary

**Title:** SSO redirect loop after Okta-to-Entra-ID migration for users with plus-addressed or aliased emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Microsoft but are immediately redirected back to the identity provider upon returning to TaskFlow. The issue correlates with users who have plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses were set up as aliases during the Entra ID migration, rather than being primary emails.

## Root Cause Hypothesis
TaskFlow's session or identity matching logic likely compares the email claim returned by the IdP against stored user records using an exact string match. Entra ID is probably returning the user's primary email address in the token claims, which does not match the plus-addressed or aliased email stored in TaskFlow's user table. The failed match prevents session creation, so TaskFlow treats the user as unauthenticated and redirects back to the IdP, creating the loop. Under Okta, the email claim may have been configured to return the exact plus-addressed or alias form.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID as the SSO provider
  2. Create or identify a user whose TaskFlow account email is a plus-addressed email (e.g., jane+taskflow@company.com) or an Entra ID alias rather than their primary Entra ID email
  3. Attempt to log in as that user via SSO
  4. Observe that after successful Microsoft authentication, the user is redirected back to Microsoft in a loop instead of landing in TaskFlow

## Environment
TaskFlow v2.3.1 (self-hosted), Microsoft Entra ID SSO, multiple browsers (Chrome, Edge, Firefox), Windows and macOS, no VPN or network variance

## Severity: high

## Impact
Approximately 30% of the team is completely unable to log into TaskFlow. This blocks all productivity for affected users with no known workaround.

## Recommended Fix
Investigate how TaskFlow matches the identity token's email claim to local user records. Check whether Entra ID returns the primary email rather than the plus-addressed or alias form in the `email` or `preferred_username` claim. Likely fixes: (1) configure the Entra ID app registration to emit the correct email claim (e.g., via optional claims or claims mapping policy), or (2) update TaskFlow's user-matching logic to normalize email addresses (strip plus-addressing suffixes, check against known aliases) before matching, or (3) update affected user records in TaskFlow to use their Entra ID primary email.

## Proposed Test Case
Create a test user in Entra ID whose primary email is user@company.com but whose TaskFlow account uses user+taskflow@company.com. Initiate SSO login and verify that the user is authenticated and a session is created without a redirect loop. Repeat with an alias email scenario.

## Information Gaps
- No server-side logs or browser network traces showing the specific token claims or session-creation failure
- Exact SAML/OIDC claim configuration in the Entra ID app registration (which claim field carries the email)
- Whether TaskFlow uses SAML or OIDC for SSO integration
