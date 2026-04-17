# Triage Summary

**Title:** SSO login redirect loop after Okta-to-Entra ID migration, likely triggered by plus-addressed emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Microsoft but are immediately redirected back to the login flow. Clearing cookies or using incognito mode sometimes resolves the issue temporarily.

## Root Cause Hypothesis
TaskFlow's session validation or token parsing likely mishandles the '+' character in plus-addressed emails (e.g., jane+taskflow@company.com). When Entra ID returns the authenticated user's email in the token claims, TaskFlow may be failing to match or store it correctly — possibly URL-encoding the '+' as a space, or failing a comparison against the stored user record. This would cause the session to never be established, triggering the redirect loop. The fact that clearing cookies sometimes helps suggests stale Okta session cookies may also interfere with the new Entra ID flow for some users.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 self-hosted with Microsoft Entra ID SSO
  2. Create or use a user account with a plus-addressed email (e.g., user+taskflow@company.com)
  3. Attempt to log in via SSO
  4. Observe: authentication succeeds at Microsoft but TaskFlow enters an infinite redirect loop
  5. Optional: clear all cookies or use incognito and retry — may work intermittently

## Environment
TaskFlow v2.3.1, self-hosted. Mixed browsers (Chrome, Edge, Firefox) and OS (Windows, macOS). SSO provider: Microsoft Entra ID (migrated from Okta).

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. Workarounds (clearing cookies, incognito) are unreliable and disruptive to daily workflows.

## Recommended Fix
1. Inspect how TaskFlow parses the email claim from the Entra ID OIDC/SAML token — check whether the '+' character is being URL-decoded as a space or otherwise mangled. 2. Check the session establishment logic for email matching against the user database — ensure plus-addressed emails are compared correctly. 3. Review cookie handling for leftover Okta session cookies that may conflict with the new Entra ID flow (domain, path, name collisions). 4. Add explicit handling/normalization for plus-addressed emails in the SSO callback.

## Proposed Test Case
Write an integration test that authenticates via SSO with a plus-addressed email (e.g., test+tag@example.com) and verifies that: (a) the session is established successfully without redirect loops, (b) the stored user email matches the original plus-addressed form, and (c) no stale cookies from a prior SSO provider interfere with the flow.

## Information Gaps
- Server-side logs from TaskFlow during a failed login attempt (would confirm where the loop breaks)
- Whether the handful of affected users without plus addresses share another distinguishing trait (e.g., special characters in email, old cached tokens)
- Exact Entra ID token claim format for plus-addressed emails (email vs preferred_username vs UPN)
