# Triage Summary

**Title:** Infinite redirect loop during SSO login when Entra ID email claim doesn't match stored user email

## Problem
After migrating from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Entra but TaskFlow immediately redirects them back to the SSO provider instead of establishing a session. The loop is rapid and produces no user-facing error message.

## Root Cause Hypothesis
TaskFlow's SSO callback handler looks up users by matching the email claim in the identity token against stored user records. When no match is found, instead of returning an error, the code treats the user as unauthenticated and redirects back to the SSO provider — which, seeing a valid session, immediately redirects back with the same token, creating the loop. The email mismatches have two sources: (1) Plus-addressed users (e.g. jane+taskflow@company.com) — Okta was configured to send the plus-addressed form, but Entra ID likely sends the primary address (jane@company.com). (2) Migrated/aliased users — their Entra primary email differs from the old email TaskFlow has stored from the Okta era. The intermittent cookie-clearing workaround may work in cases where it forces a fresh authentication flow that triggers a slightly different code path, but doesn't persist because the underlying email mismatch remains.

## Reproduction Steps
  1. Identify a user whose TaskFlow account email differs from their Entra ID primary email (e.g. a plus-addressed email or a pre-migration email alias)
  2. Have that user attempt to log into TaskFlow via SSO
  3. Observe the browser rapidly redirecting between TaskFlow's login endpoint and Microsoft's authentication endpoint in a loop
  4. Optionally: compare by logging in as a user whose TaskFlow email matches their Entra ID primary email — login succeeds

## Environment
TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers (Chrome, Edge, Firefox). Approximately 30% of users affected. Affected users can authenticate to other Entra-integrated apps without issue.

## Severity: high

## Impact
~30% of the user base cannot reliably log into TaskFlow. Workarounds (clearing cookies, incognito) are unreliable and temporary. This is a complete blocker for affected users' daily work in the application.

## Recommended Fix
1. **Immediate:** In the SSO callback handler, add an explicit error response when the email claim from the identity token doesn't match any stored user — return a clear error page (e.g. 'No account found for this email') instead of redirecting back to the SSO provider. This breaks the loop and makes the problem visible. 2. **Root cause fix:** Normalize email matching to be case-insensitive and plus-address-aware (strip the +suffix portion before lookup), OR update stored user emails to match what Entra ID returns, OR configure Entra ID app registration to send the plus-addressed email in the claim (via custom claims mapping). 3. **Migration cleanup:** Audit all user records to identify any remaining email mismatches between TaskFlow's stored emails and Entra ID primary emails, especially for users whose addresses changed during migration. Update TaskFlow records or add Entra aliases as needed.

## Proposed Test Case
Create a test user in TaskFlow with the email 'testuser+alias@company.com'. Configure Entra ID to return 'testuser@company.com' as the email claim. Attempt SSO login. Verify that (a) the login either succeeds by matching the normalized email, or (b) a clear error message is shown — but never an infinite redirect loop.

## Information Gaps
- Exact email claim attribute Entra ID is configured to send (e.g. preferred_username, email, UPN) — a developer can check the Entra app registration and TaskFlow's OIDC config
- Whether TaskFlow's auth flow has a JIT (just-in-time) provisioning or account-creation path that might be relevant
- Server-side logs during the redirect loop — may reveal the specific lookup failure or session-establishment error
- The exact list of affected users without plus-addressing or email changes — confirming whether other mismatch patterns exist beyond these two
