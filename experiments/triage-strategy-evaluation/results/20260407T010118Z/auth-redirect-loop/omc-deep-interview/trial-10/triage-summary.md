# Triage Summary

**Title:** Infinite login redirect loop after Okta-to-Entra ID SSO migration — likely session cookie rejection (SameSite) combined with email identity mismatch

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully with Microsoft, get redirected back to TaskFlow, but TaskFlow immediately redirects them back to Microsoft. Clearing cookies helps temporarily but the issue returns on next login. The remaining 70% of users can log in normally.

## Root Cause Hypothesis
Two likely contributing factors: (1) **Session cookie rejection:** Browser dev tools show a warning flag on the Set-Cookie header in TaskFlow's auth callback response, likely a SameSite attribute issue. If the session cookie is silently dropped, TaskFlow sees no session and redirects back to the IdP. The SSO migration may have changed the auth callback flow timing or redirect chain in a way that triggers SameSite=Lax enforcement (e.g., the callback is now a cross-site POST rather than a same-site GET). (2) **Email identity mismatch:** The 30% selectivity correlates with users who have non-canonical email identities — plus-addressed emails (jane+taskflow@company.com), or users whose emails were changed/aliased (personal email → company email). Entra ID may return a different email claim than what Okta returned, causing TaskFlow's user lookup to fail silently after the cookie is set, which could also trigger the redirect. Note: removing the plus address didn't fix it for one test user (Sarah), suggesting the cookie issue is the primary mechanism, with email mismatch as a possible secondary factor or the plus-address theory is a red herring and the real common factor is the email aliasing/migration history.

## Reproduction Steps
  1. Identify an affected user (likely one with plus-addressed email or history of email changes in the identity provider)
  2. Have the user clear all TaskFlow cookies and close all TaskFlow tabs
  3. Have the user navigate to taskflow.company.com and click Login
  4. User authenticates successfully with Microsoft Entra ID
  5. User is redirected back to TaskFlow and immediately redirected to Microsoft again in a loop
  6. Open browser dev tools Network tab during the loop and inspect the Set-Cookie header on TaskFlow's auth callback response — look for warning icons and SameSite/Secure/Domain attributes
  7. Compare: try the same flow in a fresh incognito window — it may intermittently succeed

## Environment
TaskFlow hosted at taskflow.company.com over HTTPS. SSO recently migrated from Okta to Microsoft Entra ID. URL and domain unchanged during migration. Users primarily on Chrome and Edge. Issue is not browser-specific.

## Severity: high

## Impact
~30% of the team is locked out of TaskFlow with no reliable workaround. Clearing cookies provides only temporary relief. Affects users across browsers. Business impact is significant as a substantial portion of the team cannot access their task management tool.

## Recommended Fix
Investigation path, in priority order:

1. **Inspect session cookie attributes:** Check the Set-Cookie header on TaskFlow's OIDC callback endpoint. Verify SameSite, Secure, Domain, and Path attributes. If SameSite=None, ensure Secure is also set. If SameSite=Lax, ensure the callback uses a top-level GET navigation (not a POST or iframe). The SSO library configuration may need updating for the new Entra ID flow.

2. **Check auth callback logs:** Add or review logging at the OIDC callback handler. After receiving the token from Entra ID, log: the email/sub claim from the token, the user lookup result, and whether a session is successfully created. Look for 'user not found' or silent failures.

3. **Compare token claims:** Diff the OIDC token claims between Okta and Entra ID for an affected user. Key fields: email, preferred_username, sub, upn. TaskFlow may be keying on a claim that Entra ID populates differently than Okta (e.g., Okta sent the plus-addressed email in `email`, Entra sends the canonical email in `email` but the plus-addressed one in `preferred_username`).

4. **Check for stale OIDC configuration:** Ensure TaskFlow's OIDC client config (issuer, token endpoint, JWKS URI) is fully updated for Entra ID and that no Okta-specific settings remain.

## Proposed Test Case
Create integration tests for the OIDC callback handler that verify: (a) session cookie is set with correct SameSite/Secure/Domain attributes and persists across a simulated redirect, (b) user lookup succeeds for plus-addressed emails, aliased emails, and emails with changed canonical forms, (c) the full login → IdP redirect → callback → session creation → authenticated page flow completes without looping for users with non-standard email formats.

## Information Gaps
- Exact cookie attributes (SameSite, Domain, Path, Secure) on TaskFlow's auth callback Set-Cookie header — reporter saw a warning but couldn't read details
- Server-side auth callback logs showing what happens after Entra ID redirects back (user lookup result, session creation success/failure)
- Whether the OIDC callback is handled as a GET or POST redirect — this affects SameSite behavior
- The exact OIDC claims mapping configuration in TaskFlow (which claim is used for user identity matching)
- Whether the 30% affected users truly share a common trait beyond plus-addressing (email aliasing history is a lead but unconfirmed)
