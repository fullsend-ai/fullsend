# Triage Summary

**Title:** Login redirect loop for users whose stored email doesn't match Entra ID email claim (plus-addressing and alias mismatches)

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully with Entra ID but TaskFlow cannot match the returned identity to an existing account, causing it to restart the login flow. The affected population consists primarily of users with plus-addressed emails (e.g., jane+taskflow@company.com) and users whose primary email changed during the migration (Okta alias vs. Entra primary email).

## Root Cause Hypothesis
TaskFlow's OIDC callback handler performs an exact-match lookup of the `email` (or `preferred_username`) claim from the Entra ID token against its user table. Entra ID returns the user's primary/canonical email (jane@company.com), which does not match the plus-addressed or aliased email stored in TaskFlow (jane+taskflow@company.com). The lookup fails, and instead of surfacing an error, the application re-initiates the SSO flow — creating an infinite redirect loop. The stale session cookie from each failed attempt likely contributes to the loop persisting in non-incognito browsers, which explains why incognito occasionally breaks the cycle.

## Reproduction Steps
  1. Have a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com) or an email that differs from their Entra ID primary email
  2. Attempt to log in via the Microsoft Entra ID SSO flow
  3. Authenticate successfully in Entra ID
  4. Observe that TaskFlow redirects back to Entra ID instead of completing login, creating an infinite loop

## Environment
TaskFlow with OIDC/SSO integration, recently migrated from Okta to Microsoft Entra ID. Affects all browsers (Chrome, Edge, Firefox). Approximately 30% of users affected.

## Severity: high

## Impact
~30% of the team is locked out of TaskFlow entirely (unless they happen to get lucky with incognito). This is a blocking issue for daily work for those users.

## Recommended Fix
1. In the OIDC callback handler, change the user lookup to normalize emails before matching — at minimum, strip the plus-addressed portion (everything between `+` and `@`) before comparing. 2. Additionally, consider storing the Entra ID `sub` (subject) claim as a stable user identifier rather than relying solely on email matching, since emails can change across IdP migrations. 3. As an immediate workaround, update the affected users' emails in TaskFlow's database to match what Entra ID returns, or configure Entra ID to emit the plus-addressed email via a custom claim mapping.

## Proposed Test Case
Create test users with plus-addressed emails (user+tag@domain.com) and emails that differ from the IdP's primary email. Verify that the OIDC callback correctly matches these users to their accounts after authentication. Also verify that when no match is found, the application surfaces a clear error rather than entering a redirect loop.

## Information Gaps
- Exact OIDC claim TaskFlow uses for user lookup (email vs. preferred_username vs. sub) — inspecting the codebase will clarify this
- Whether TaskFlow has a user-creation-on-first-login flow that might interact with the mismatch
- The precise behavior difference in incognito — likely just clean cookie state, but could confirm by inspecting the redirect chain with DevTools
