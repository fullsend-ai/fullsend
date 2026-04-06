# Triage Summary

**Title:** Infinite login redirect loop for users whose TaskFlow email doesn't match Entra ID primary email (plus-addressing / alias mismatch)

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. These users authenticate successfully with Entra ID but are immediately redirected back to the IdP instead of landing in TaskFlow. The affected users are those whose email stored in TaskFlow (often a plus-address like jane+taskflow@company.com, or a legacy alias) differs from the primary email Entra ID returns in the token (jane@company.com).

## Root Cause Hypothesis
TaskFlow performs user lookup using the email claim from the OIDC/SAML token. When Entra ID returns the user's primary email but TaskFlow has the user stored under a plus-address or alias, the lookup fails. Instead of returning an authentication error, TaskFlow's auth middleware fails to establish a valid session — the session cookie is either not set or immediately invalidated — which causes the middleware to treat the user as unauthenticated on the next request and restart the SSO flow, creating the loop. The cookie-then-no-cookie behavior observed in browser dev tools confirms the session is not being persisted after the failed user lookup.

## Reproduction Steps
  1. Deploy TaskFlow v2.3.1 self-hosted with Entra ID as the SSO provider
  2. Create a user in TaskFlow whose stored email is a plus-address (e.g., jane+taskflow@company.com)
  3. Ensure the corresponding Entra ID account's primary email is the base address (jane@company.com)
  4. Attempt to log in as that user via SSO
  5. Observe the infinite redirect loop between TaskFlow and Entra ID
  6. Compare with a user whose TaskFlow email matches their Entra ID primary email — login works normally

## Environment
TaskFlow v2.3.1, self-hosted. SSO provider: Microsoft Entra ID (migrated from Okta). Affects users with plus-addressing or email aliases where stored email differs from Entra ID primary email.

## Severity: high

## Impact
Approximately 30% of the team is unable to reliably log into TaskFlow. No consistent workaround exists — clearing cookies and incognito mode are unreliable. This blocks daily work for affected users.

## Recommended Fix
1. Identify which claim TaskFlow uses for user lookup (likely `email` — check the OIDC/SAML config). 2. Fix the user lookup to handle the mismatch — either normalize emails by stripping plus-addressing before lookup, or add a secondary lookup by alias/alternate email. 3. Fix the silent failure path: when user lookup fails after successful IdP authentication, TaskFlow should display an error ('Account not found for email jane@company.com') rather than failing to set a session and restarting the auth flow — this is the direct cause of the loop. 4. As an immediate mitigation, update affected users' emails in TaskFlow to match their Entra ID primary email. 5. Consider adding a configurable claim mapping so admins can choose which Entra ID claim to match against.

## Proposed Test Case
Create integration tests for the SSO callback handler: (a) user with matching email logs in successfully, (b) user with plus-address mismatch receives a clear error message instead of a redirect loop, (c) if alias/normalized lookup is implemented, user with plus-address is matched correctly to their account. Also add a unit test verifying the session is established (or an error is raised) after the IdP callback — never a silent no-op.

## Information Gaps
- Exact claim TaskFlow is configured to use for user lookup (likely email, but not confirmed)
- Whether TaskFlow's auth handler has any logging for failed user lookups that could confirm this diagnosis
- Whether the session cookie issue is a separate bug in cookie handling (e.g., SameSite or Secure flag misconfiguration) or purely a consequence of the failed user lookup
