# Triage Summary

**Title:** SSO redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are caught in an infinite authentication redirect loop. Users authenticate successfully with Entra ID but TaskFlow immediately redirects them back to the IdP. Affected users correlate with plus-addressed emails (user+taskflow@company.com) and users whose email addresses changed (aliases). Clearing cookies or incognito mode occasionally breaks the loop temporarily.

## Root Cause Hypothesis
TaskFlow matches authenticated users by comparing the email claim in the OIDC/SAML token against stored user records. Okta likely returned the plus-addressed or alias email in the token's email claim (matching what TaskFlow stored), while Entra ID returns the canonical primary email from the directory. When TaskFlow receives an email claim that doesn't match any stored user, it cannot establish a session and redirects back to the IdP, creating the loop. The intermittent cookie/incognito fix suggests residual Okta session cookies may also interfere with the new auth flow.

## Reproduction Steps
  1. Set up a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com)
  2. Ensure that user's Entra ID primary email is the base address (user@company.com)
  3. Attempt to log into TaskFlow via SSO
  4. Observe: Entra ID authentication succeeds, redirect back to TaskFlow occurs, but TaskFlow immediately redirects to Entra ID again in a loop

## Environment
TaskFlow with SSO integration, recently migrated from Okta to Microsoft Entra ID. All user accounts were migrated (none created fresh in Entra). Redirect URIs verified correct in Entra app registration.

## Severity: high

## Impact
~30% of users are completely unable to log into TaskFlow. No reliable workaround exists — clearing cookies and incognito mode work only intermittently. Affected users can authenticate with other Entra ID apps, so this is blocking only TaskFlow access.

## Recommended Fix
1. Inspect the OIDC/SAML token claims from Entra ID for an affected user vs. an unaffected user — compare the 'email', 'preferred_username', and 'upn' claims. 2. Check which claim TaskFlow uses for user matching (likely 'email') and what value is stored in the TaskFlow user table for affected accounts. 3. Fix the mismatch by either: (a) configuring Entra ID to send the plus-addressed/alias email in the token claim (via optional claims or claims mapping policy), or (b) updating TaskFlow's user-matching logic to normalize emails (strip plus-addressing, check aliases) or match on a stable identifier like 'sub' or 'oid' instead of email. 4. Clear any residual Okta session cookies by invalidating old session tokens or updating the session cookie domain/name.

## Proposed Test Case
Create test users with three email scenarios: (1) standard email, (2) plus-addressed email, (3) aliased/changed email. Authenticate each via Entra ID SSO and verify all three successfully establish a TaskFlow session without redirect loops. Additionally, verify that a user with stale Okta cookies in their browser can authenticate cleanly.

## Information Gaps
- Exact OIDC/SAML claim configuration in TaskFlow's auth module (which claim is used for user matching)
- Whether TaskFlow uses 'email', 'upn', 'preferred_username', or 'sub' as the user identifier
- Entra ID optional claims configuration for the TaskFlow app registration
- Whether any affected users lack the plus-address or alias pattern (could indicate additional causes)
