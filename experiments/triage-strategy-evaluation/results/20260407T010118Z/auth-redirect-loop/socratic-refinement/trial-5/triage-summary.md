# Triage Summary

**Title:** Session cookie with SameSite=Strict is dropped on cross-origin SSO redirect from Microsoft Entra ID, causing infinite login loop

## Problem
After migrating from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully with Microsoft, gets redirected back to TaskFlow, but the session cookie set by TaskFlow is silently discarded by the browser, causing TaskFlow to redirect them back to Microsoft again.

## Root Cause Hypothesis
TaskFlow sets its session cookie with `SameSite=Strict`, which instructs browsers to reject cookies on cross-site navigations. Since SSO login involves a redirect from Microsoft's domain back to TaskFlow's domain, this is a cross-site navigation and the cookie is dropped. The reason only ~30% of users are affected is likely due to differences in the redirect chain: some users may follow a flow that includes an intermediate same-site redirect (e.g., a TaskFlow callback endpoint that redirects to the app's main page, making the final cookie-setting request same-site), while affected users hit a flow where the cookie is set directly on the cross-site redirect response. The plus-sign email correlation may indicate these users were provisioned or routed through a different auth path.

## Reproduction Steps
  1. Set up TaskFlow with Microsoft Entra ID SSO
  2. Log in as an affected user (or any user if the issue is consistent in a test environment)
  3. Click the login button — user is redirected to Microsoft
  4. Authenticate successfully with Microsoft
  5. Observe the redirect back to TaskFlow — the Set-Cookie header is present in the response but the cookie is not stored in the browser
  6. TaskFlow detects no session cookie and redirects back to Microsoft, creating an infinite loop
  7. Confirm by checking Application → Cookies in dev tools: the session cookie is missing for the affected user but present for an unaffected user

## Environment
Self-hosted TaskFlow instance, SSO via Microsoft Entra ID (recently migrated from Okta), affects ~30% of users across Chrome/Edge/Firefox, correlated with plus-sign email addresses but not exclusively

## Severity: high

## Impact
Approximately 30% of the team cannot log into TaskFlow at all through normal flow. Some can work around it via incognito windows (intermittently), but there is no reliable workaround.

## Recommended Fix
Change the session cookie's `SameSite` attribute from `Strict` to `Lax`. `SameSite=Lax` permits cookies on top-level navigational redirects (like SSO callbacks) while still protecting against CSRF on subresource requests. Investigate where the cookie's SameSite policy is configured — this is likely in TaskFlow's session/authentication middleware configuration. If TaskFlow uses a framework session library, check its cookie configuration options. If `SameSite=None` is needed instead (e.g., for iframe scenarios), ensure the `Secure` flag is also set. Additionally, investigate why only some users trigger the problematic flow — there may be multiple redirect paths in the auth code, and the one affecting 30% of users may differ from the main path.

## Proposed Test Case
Write an integration test that simulates a cross-origin SSO redirect callback (request with an Origin/Referer from a different domain) and verifies that the session cookie set in the response has `SameSite=Lax` (not `Strict`). Additionally, verify that the cookie is accepted by the browser in an end-to-end test that completes the full SSO redirect flow.

## Information Gaps
- Why exactly 30% of users are affected while others with the same SameSite=Strict cookie work fine — there may be multiple auth/redirect code paths
- Whether the plus-sign email correlation is causal or coincidental (it may affect which auth code path is taken)
- Server-side auth logs were never retrieved — these could confirm whether TaskFlow receives and validates the identity token at all before setting the cookie
- The exact TaskFlow version and authentication middleware/library in use
