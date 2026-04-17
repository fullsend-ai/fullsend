# Triage Summary

**Title:** SSO redirect loop after Okta-to-Entra migration for users with plus-addressed or aliased emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft but TaskFlow fails to resolve their identity, sending them back to the IdP. The affected population correlates strongly with users who have plus signs in their email addresses (e.g., jane+taskflow@company.com) or who have had email address changes/aliases applied to their accounts.

## Root Cause Hypothesis
TaskFlow's user lookup after SSO callback likely matches the email claim from the identity token against stored user records. Two problems are probable: (1) Entra ID may encode or normalize the '+' character differently than Okta did in the email claim (e.g., URL-encoding as %2B, or stripping the plus-addressed portion entirely), causing a mismatch against the stored email. (2) For aliased users, Entra ID may return a different email (the primary or a different alias) than Okta did, so the lookup fails. When the lookup fails, TaskFlow has no authenticated session, so it redirects back to login, creating the loop. The intermittent incognito success suggests stale session cookies or cached OIDC state from the old Okta integration may compound the issue by corrupting the auth flow.

## Reproduction Steps
  1. Configure TaskFlow SSO with Microsoft Entra ID
  2. Create or have a user whose email contains a plus sign (e.g., user+tag@company.com)
  3. Attempt to log in via SSO
  4. Observe successful authentication at Microsoft followed by redirect back to TaskFlow login page in a loop
  5. Also test with a user whose primary email in Entra differs from what was stored in TaskFlow from the Okta era

## Environment
TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Occurs across Chrome, Edge, and Firefox. Not role-dependent.

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all, completely blocking their access to the application. Workarounds (incognito) are unreliable.

## Recommended Fix
1. Inspect the OIDC token claims Entra ID returns for affected users — compare the email/preferred_username/UPN claim value against what TaskFlow has stored. Log the exact claim value at the callback handler. 2. Check whether TaskFlow's user-matching logic URL-decodes or normalizes the email before lookup. If '+' is being encoded as %2B by Entra, the lookup code needs to decode it. 3. Consider matching on a more stable claim (such as the OIDC 'sub' or 'oid' claim) rather than email, or add fallback matching on email aliases. 4. For the stale-cookie issue: invalidate all existing sessions from the Okta era, or add logic to clear SSO-related cookies when the auth loop is detected (e.g., after N redirects within a short window).

## Proposed Test Case
Write integration tests for the SSO callback user-lookup function that cover: (a) email with plus addressing (user+tag@domain.com), (b) URL-encoded plus (%2B), (c) email that doesn't match stored value but matches a known alias, (d) verify that a failed lookup does not produce an infinite redirect but instead shows an actionable error page.

## Information Gaps
- Exact OIDC claim (email vs preferred_username vs UPN) TaskFlow uses for user matching has not been confirmed
- Whether any affected users lack plus signs AND have never had aliases (would weaken the hypothesis)
- Server-side logs from the callback handler showing the actual claim values have not been examined yet
