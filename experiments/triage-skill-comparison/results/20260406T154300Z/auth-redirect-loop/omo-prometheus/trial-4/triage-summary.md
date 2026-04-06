# Triage Summary

**Title:** Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra SSO migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Microsoft but are immediately redirected back to the login page. Affected users are predominantly those with plus-addressed emails (e.g., jane+taskflow@company.com) or emails that were aliased/changed during the Entra migration.

## Root Cause Hypothesis
Two compounding issues: (1) **Email claim mismatch**: Entra ID returns the user's canonical/primary email in the token (e.g., jane@company.com), whereas Okta returned the plus-addressed form (jane+taskflow@company.com). TaskFlow's user lookup in the auth callback uses exact string matching against the stored email, so it fails to find the user. (2) **SameSite cookie issue on cross-origin redirect**: The session cookie's SameSite attribute is likely set to 'Lax' or 'Strict' (or defaulting to 'Lax' per modern browser defaults). When the browser is redirected back from Microsoft's domain to TaskFlow, this is a cross-site navigation, and the browser refuses to persist the cookie. This means even if user lookup succeeded, the session wouldn't stick — causing the loop. The two issues may affect overlapping but distinct subsets of the ~30% affected users.

## Reproduction Steps
  1. Have a user account in TaskFlow whose stored email uses plus-addressing (e.g., jane+taskflow@company.com)
  2. Ensure the corresponding Entra ID account's primary email is the canonical form (jane@company.com)
  3. Attempt to log in to TaskFlow via SSO
  4. Observe: user authenticates with Microsoft successfully, is redirected back to TaskFlow, but is immediately redirected to Microsoft again in a loop
  5. Check browser dev tools Network tab: confirm the auth callback response contains a Set-Cookie header but the cookie is not saved by the browser

## Environment
TaskFlow web application with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected. The issue is not browser-specific but incognito/cookie-clearing provides inconsistent temporary relief.

## Severity: high

## Impact
~30% of users are completely unable to log into TaskFlow reliably. No dependable workaround exists — clearing cookies and incognito mode are inconsistent. This is a production blocker for affected users.

## Recommended Fix
Address both root causes: (1) **Email matching**: Modify TaskFlow's auth callback to normalize email comparison — strip plus-address suffixes and/or perform case-insensitive lookup. Better yet, match users on a stable unique identifier (like the Entra Object ID / `sub` or `oid` claim) rather than email, and add a one-time migration to populate that field from current sessions or a bulk mapping. As a quick fix, update the stored emails for affected users to match what Entra sends. (2) **Session cookie**: Set the session cookie's SameSite attribute to `None` and ensure the `Secure` flag is set (required for SameSite=None). Verify the cookie's Domain and Path attributes are correct for the redirect target URL. Check that the response also includes appropriate CORS headers if needed.

## Proposed Test Case
Create a test user with a plus-addressed email stored in TaskFlow's database. Mock the SSO token to return the canonical (non-plus) email. Verify that the auth callback successfully matches the user, creates a valid session, and the session cookie is persisted across the redirect. Confirm no redirect loop occurs. Additionally, write a unit test for the email normalization logic to ensure plus-addressing, case differences, and alias variations all resolve to the correct user.

## Information Gaps
- Exact SameSite and Secure attribute values currently set on TaskFlow's session cookie
- Whether TaskFlow uses OIDC or SAML with Entra (affects redirect method: POST vs GET, which impacts SameSite behavior differently)
- Whether any reverse proxy or CDN in front of TaskFlow is stripping or modifying Set-Cookie headers
- Full application-level auth logs showing the exact failure point in the callback handler
