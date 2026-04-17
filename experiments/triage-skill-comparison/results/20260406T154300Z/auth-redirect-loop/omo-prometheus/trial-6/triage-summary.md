# Triage Summary

**Title:** SSO redirect loop for users with plus-addressed or aliased emails after Okta → Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Microsoft but TaskFlow fails to establish a session, redirecting them back to the identity provider repeatedly. The issue correlates strongly with users who have plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses changed and have aliases configured in Entra ID.

## Root Cause Hypothesis
TaskFlow's user-matching logic compares an email claim from the SSO token against stored user records. Okta likely returned the plus-addressed or alias email as the primary email claim, matching what TaskFlow has on file. Entra ID is returning a different value — most likely the primary/canonical UPN or mail attribute, which strips the plus-address tag or uses the new primary email instead of the alias. TaskFlow fails to find a matching user, does not create a session, and redirects back to the IdP, creating the loop. The intermittent success after clearing cookies suggests a partially-written or invalid session cookie is also contributing to the loop persistence.

## Reproduction Steps
  1. Configure a user account in Entra ID with a plus-addressed email (e.g., user+taskflow@company.com) or an email alias
  2. Ensure the corresponding TaskFlow user record stores that plus-addressed or alias email
  3. Attempt to log into TaskFlow via SSO
  4. Observe: authentication succeeds at Microsoft, redirect back to TaskFlow occurs, then immediately redirected to Microsoft again in a loop
  5. Compare the email claim value in the OIDC/SAML token against the email stored in TaskFlow's user table to confirm the mismatch

## Environment
TaskFlow with Microsoft Entra ID SSO (migrated from Okta). Affects all browsers (Chrome, Edge, Firefox). Not OS-specific. Users with simple, unchanged email addresses are unaffected.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. Incognito windows provide a fragile workaround but the problem recurs on subsequent sessions. This blocks daily work for affected users.

## Recommended Fix
1. Inspect the OIDC/SAML token claims returned by Entra ID for affected users — compare the `email`, `preferred_username`, and `upn` claims against what TaskFlow stores. 2. Identify which claim TaskFlow uses for user lookup and verify it matches what Entra ID sends. 3. Fix the user-matching logic to normalize email comparison: strip plus-address tags before matching, and/or query against email aliases, not just the primary stored email. 4. Consider adding a fallback match on a stable, immutable identifier (e.g., Entra Object ID mapped to an external IdP ID field) rather than relying solely on email matching. 5. Fix the session handling so a failed user lookup returns an explicit error page instead of silently redirecting back to the IdP.

## Proposed Test Case
Create test users with three email configurations: (a) simple email matching in both IdP and TaskFlow, (b) plus-addressed email stored in TaskFlow with canonical email returned by Entra ID, (c) aliased email stored in TaskFlow with primary email returned by Entra ID. Verify all three users can complete the SSO login flow and land on the TaskFlow dashboard without redirect loops.

## Information Gaps
- Exact claim(s) TaskFlow reads from the SSO token (email vs. upn vs. preferred_username)
- Whether TaskFlow stores the Okta-era email verbatim or has a separate IdP subject ID field
- Server-side logs showing the specific point where the session creation fails
