# Triage Summary

**Title:** SSO redirect loop after Okta-to-Entra ID migration for users with '+' in email or email mismatch

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Microsoft but are immediately redirected back to the identity provider. The loop consists of 302 redirects until the browser errors with 'too many redirects'. A session cookie appears to be set during the redirect chain but does not persist on subsequent requests.

## Root Cause Hypothesis
TaskFlow's user-lookup or session-creation logic likely fails when the email claim from Entra ID contains a '+' character or does not exactly match the stored user email. The most probable cause is that the email is being URL-decoded, truncated at the '+', or otherwise normalized differently than Entra ID sends it, causing the user lookup to fail silently. When the lookup fails, TaskFlow cannot establish a session (explaining the disappearing cookie), and redirects back to the IdP to re-authenticate, creating the loop. A secondary population may be affected by email mismatches between their Entra ID profile and their original TaskFlow account (e.g., email changed in one system but not the other).

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO
  2. Create a test user in Entra ID with a plus-addressed email (e.g., testuser+tag@company.com)
  3. Ensure a corresponding account exists in TaskFlow for that email
  4. Attempt to log in as that user via SSO
  5. Observe the infinite 302 redirect loop between TaskFlow and Microsoft

## Environment
TaskFlow v2.3.1, self-hosted on Ubuntu (likely 22.04), SSO via Microsoft Entra ID (likely OIDC)

## Severity: high

## Impact
Approximately 30% of the team is completely unable to log into TaskFlow. This is a total access blocker for affected users with no known workaround.

## Recommended Fix
Investigate the SSO callback handler in TaskFlow's authentication code. Specifically: (1) Check how the email claim from the OIDC token is parsed and matched against stored user records — look for URL-encoding issues with the '+' character ('+' may be decoded as a space). (2) Check whether the session/cookie is only set after a successful user lookup, and whether a failed lookup silently redirects rather than returning an error. (3) Consider adding a case-insensitive, normalized email comparison that handles plus-addressing. (4) For email-mismatch cases, check whether TaskFlow matches on a stored immutable identifier (like an OIDC 'sub' claim) rather than relying solely on email matching.

## Proposed Test Case
Create automated tests for the SSO callback handler that verify: (a) a user with a '+' in their email can complete authentication without looping, (b) the session cookie is correctly set and persists after the callback, (c) an email claim that doesn't match any stored user results in a clear error page rather than a redirect loop, and (d) email matching is consistent regardless of URL-encoding of special characters.

## Information Gaps
- Exact authentication protocol (OIDC vs SAML) — reporter needs to confirm with DevOps
- Server-side logs during the redirect loop — reporter needs to involve their server admin
- Whether the non-plus-addressed affected users have an email mismatch between Entra ID and TaskFlow
- Exact Ubuntu version (reported as ~22.04 but unconfirmed)
