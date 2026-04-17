# Triage Summary

**Title:** SSO login redirect loop caused by SameSite=Strict session cookie being dropped on cross-origin callback

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully at Entra ID, gets redirected back to TaskFlow's callback URL, TaskFlow sets a session cookie, but the browser discards it because SameSite=Strict is incompatible with cross-origin redirects. The next request has no session, so TaskFlow redirects back to Entra ID, creating the loop.

## Root Cause Hypothesis
TaskFlow's session cookie is set with SameSite=Strict. Per the SameSite cookie spec, Strict cookies are not sent (and are silently dropped when set) on responses to cross-site navigations — which is exactly what an OIDC/SAML callback redirect from login.microsoftonline.com to the TaskFlow domain is. This likely worked with Okta due to differences in redirect flow timing, intermediate redirects, or the previous cookie configuration. The 30% affected users are likely those whose pre-migration sessions have expired, forcing a fresh SSO flow; the remaining 70% are still using valid session cookies from before the cutover and will hit this issue as those sessions expire.

## Reproduction Steps
  1. Ensure no existing TaskFlow session cookie is present (clear cookies or use incognito)
  2. Navigate to TaskFlow login page on v2.3.1 self-hosted instance configured with Microsoft Entra ID SSO
  3. Click login — user is redirected to Entra ID
  4. Authenticate successfully at Entra ID
  5. Observe redirect back to TaskFlow callback URL (e.g., /auth/callback or /sso/callback)
  6. In browser dev tools Network tab, observe Set-Cookie header on callback response has SameSite=Strict
  7. Observe the cookie is not present on the subsequent request, causing TaskFlow to redirect back to Entra ID
  8. Loop repeats indefinitely

## Environment
TaskFlow v2.3.1, self-hosted, HTTPS, Microsoft Entra ID SSO (migrated from Okta), Chrome browser confirmed affected

## Severity: high

## Impact
Currently ~30% of users cannot log in. This will likely grow to 100% as existing session cookies expire. Workarounds (clearing cookies, incognito) are temporary and unreliable. Users are fully blocked from accessing TaskFlow when affected.

## Recommended Fix
Change the session cookie's SameSite attribute from Strict to Lax. SameSite=Lax permits cookies on top-level cross-origin navigations (like SSO redirects) while still protecting against CSRF on sub-requests. This is the correct setting for applications using SSO. Check TaskFlow's auth/session configuration (likely in the web framework or auth middleware config) for the SameSite setting. If TaskFlow does not expose this as a config option in v2.3.1, check for a newer version that does, or patch the session middleware directly. Also investigate whether the plus-sign email addresses (e.g., jane+taskflow@company.com) cause a secondary user-matching issue — once the cookie fix is in place, test login with a plus-sign email user to confirm no email-claim mismatch exists.

## Proposed Test Case
1. Clear all TaskFlow cookies. 2. Initiate SSO login via Entra ID. 3. Verify the callback response sets a session cookie with SameSite=Lax. 4. Verify the cookie persists on the subsequent request and the user lands on the authenticated dashboard without a redirect loop. 5. Repeat with a user whose email contains a plus sign to rule out email-matching issues.

## Information Gaps
- Server-side auth logs were not examined — there may be additional errors logged during the callback that reveal a secondary issue (e.g., email claim mismatch for plus-sign addresses)
- The exact OIDC claim TaskFlow v2.3.1 uses for user matching is unconfirmed — if it uses the email claim, plus-sign addresses may cause lookup failures that are masked by the cookie issue
- Whether the SameSite=Strict setting is a TaskFlow default or was explicitly configured is unknown — this affects whether a config change or code patch is needed
- Why the Okta flow did not trigger the same issue is not fully understood — may be a difference in redirect chain or cookie config that was overwritten during migration
