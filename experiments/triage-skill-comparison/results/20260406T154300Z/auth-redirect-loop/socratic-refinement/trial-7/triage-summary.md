# Triage Summary

**Title:** Login redirect loop after Okta→Entra ID migration for users with plus-addressed or aliased emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication at the Entra ID side succeeds, but TaskFlow fails to establish a session and redirects back to the IdP. Affected users are those with '+' in their email addresses (plus-addressing) or those whose Entra ID email differs from the email they originally registered with in TaskFlow.

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches the email claim from the IdP response against its stored user records. Entra ID likely normalizes or encodes the '+' character in email claims differently than Okta did (e.g., URL-encoding '+' as '%2B', or stripping the plus-addressed portion per RFC 5233). When the lookup fails to find a matching user, TaskFlow cannot create a session and falls back to re-initiating the auth flow, causing the loop. For aliased users, Entra ID may return the new aliased email while TaskFlow's user table still holds the original signup email. The intermittent success with incognito/cookie-clearing suggests stale Okta session cookies also interfere — TaskFlow may attempt to validate an old Okta session token, fail, and trigger the redirect before even processing the new Entra ID response.

## Reproduction Steps
  1. 1. Set up a user account in TaskFlow with a plus-addressed email (e.g., user+tag@company.com)
  2. 2. Configure Entra ID as the SSO provider with correct redirect URIs
  3. 3. Attempt to log in as that user
  4. 4. Observe: Entra ID authentication succeeds, user is redirected back to TaskFlow, then immediately redirected back to Entra ID in a loop
  5. 5. Alternatively: create a user with email A, then alias email B in Entra ID — same loop occurs when Entra ID returns email B in the claim

## Environment
TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers. Incognito mode with cleared cookies occasionally allows login to succeed.

## Severity: high

## Impact
~30% of the team is locked out of TaskFlow entirely. These users cannot reliably log in even with workarounds (incognito is inconsistent). This blocks their daily work in the task management system.

## Recommended Fix
1. **Email matching normalization:** In the SSO callback handler, normalize both the incoming email claim and the stored user email before comparison — lowercase, decode URL-encoded characters, and optionally strip plus-addressed tags for matching purposes. 2. **Stale session cleanup:** On the SSO callback endpoint, clear any pre-existing session cookies before processing the new IdP response to prevent stale Okta tokens from interfering. 3. **Fallback matching:** If exact email match fails, attempt a secondary lookup by subject/nameID claim or by domain-stripped username to handle aliased accounts. 4. **Logging:** Add debug logging to the SSO callback to capture the exact email claim received vs. what was looked up in the database, to confirm the mismatch.

## Proposed Test Case
Create integration tests for the SSO callback handler that: (a) send an IdP response with a plus-addressed email and verify the user is matched and session is created; (b) send an IdP response where the email differs from the stored email but matches an alias; (c) send an IdP response when a stale session cookie from a different IdP is present and verify the old cookie is cleared and login succeeds.

## Information Gaps
- Exact OIDC/SAML claim name TaskFlow uses for email matching (email, preferred_username, or nameID)
- Whether TaskFlow's user table has a dedicated SSO identity column or relies solely on email matching
- Server-side logs from the SSO callback showing the exact failure point in the redirect cycle
