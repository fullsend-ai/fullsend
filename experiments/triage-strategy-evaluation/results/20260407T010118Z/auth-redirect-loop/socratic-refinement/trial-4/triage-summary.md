# Triage Summary

**Title:** Infinite login redirect loop after Okta-to-Entra ID migration due to email mismatch and session cookie not persisting

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are trapped in an infinite redirect loop: they authenticate successfully with Entra, get redirected back to TaskFlow, and are immediately sent back to Entra. The remaining 70% of users log in normally. Affected users include those with plus-sign email aliases (e.g., jane+taskflow@company.com) and users whose TaskFlow profile still has a stale personal email that differs from their Entra identity.

## Root Cause Hypothesis
Two interacting issues: (1) TaskFlow's SSO callback handler performs an email-based user lookup, and when Entra ID returns a normalized email (e.g., jane@company.com) that doesn't match what TaskFlow has stored (e.g., jane+taskflow@company.com or a legacy personal email), the lookup fails. (2) Instead of rendering an error on lookup failure, TaskFlow attempts to establish a session and redirect — but the session cookie is not persisting in the browser (likely a SameSite, Secure flag, or Domain attribute misconfiguration introduced or exposed by the SSO URL change). Since the session cookie doesn't stick, the next page load sees an unauthenticated user and redirects to Entra again, creating the loop. The incognito inconsistency is explained by incognito sometimes hitting a first-time-user code path that bypasses the strict email match, but still being subject to the same cookie persistence issue.

## Reproduction Steps
  1. Identify a user whose TaskFlow profile email differs from their Entra ID email (plus-sign alias or legacy personal email)
  2. Have that user attempt to log into TaskFlow via the Entra ID SSO flow
  3. Observe the redirect loop in the browser — URL flickers between TaskFlow and Microsoft login
  4. Open browser dev tools Network tab and observe: (a) Entra redirects back with a valid auth code/token, (b) TaskFlow sets a Set-Cookie header in its response, (c) the cookie does not appear in the browser's cookie store on subsequent requests
  5. Compare with a user whose TaskFlow email matches their Entra email exactly — they log in successfully

## Environment
TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected (Chrome, Edge). Issue is not browser-specific. Entra ID app registration and redirect URIs verified as correct.

## Severity: high

## Impact
~30% of users are completely unable to log into TaskFlow. This includes users with plus-sign email aliases and users with stale email records. Workarounds (incognito) are unreliable.

## Recommended Fix
Investigate two things in order: (1) **Cookie persistence** — Inspect the Set-Cookie headers TaskFlow sends after the SSO callback. Check for SameSite=None without the Secure flag, incorrect Domain attribute, or a cookie path that changed with the SSO migration. The SSO provider URL change from Okta to Entra may have affected cross-site cookie behavior. This is likely the direct cause of the loop vs. a clean error. (2) **Email matching logic** — The SSO callback handler should normalize emails before lookup (strip plus-sign suffixes, case-insensitive comparison) or match on a stable identifier like the OIDC `sub` claim rather than email alone. For the legacy-email users, either update their TaskFlow profiles or implement a secondary lookup by Entra's `oid` or `sub` claim. Also add a failure path that surfaces a clear error message (e.g., 'No account found for this email') instead of silently redirecting back to the SSO provider.

## Proposed Test Case
Create test users with (a) a plus-sign email alias that differs from the IdP-returned email, (b) an email that doesn't match the IdP-returned email at all, and (c) an exactly matching email. Verify that (a) and (b) either match successfully after normalization or display a clear error — never enter a redirect loop. Separately, write an integration test that asserts the session cookie Set-Cookie header has correct SameSite, Secure, and Domain attributes for the production domain.

## Information Gaps
- Exact cookie attributes (SameSite, Secure, Domain, Path) being set by TaskFlow — needs developer inspection of the Set-Cookie headers
- Whether TaskFlow uses SAML or OIDC with Entra (affects which claims are available for matching)
- Whether the cookie issue also affects the 70% who can log in (they may be getting lucky with email matches masking the cookie problem)
- Full list of affected users to confirm the email-mismatch correlation covers all cases, not just the observed subset
