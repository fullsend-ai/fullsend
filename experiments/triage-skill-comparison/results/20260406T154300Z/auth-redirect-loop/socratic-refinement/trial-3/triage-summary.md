# Triage Summary

**Title:** SSO redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully at Entra ID but TaskFlow fails to establish a session and redirects them back to the IdP. The affected population correlates with users who have plus-addressed emails (e.g. jane+taskflow@company.com) or whose primary email in Entra ID differs from what was stored in Okta due to alias mappings.

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches the email claim from the OIDC/SAML token against its stored user records. Entra ID likely returns a different email value than Okta did for these users — either stripping/handling the plus-address tag differently, or returning the new primary email instead of the alias that Okta used. When the lookup fails to find a matching user, TaskFlow cannot create a session and falls back to re-initiating the login flow, causing the loop. Residual Okta session cookies may also interfere, explaining why clearing cookies occasionally breaks the cycle.

## Reproduction Steps
  1. Configure TaskFlow SSO with Microsoft Entra ID
  2. Create a user in Entra ID whose email uses plus addressing (e.g. user+taskflow@company.com) or whose primary email differs from the email stored in TaskFlow's user table
  3. Attempt to log in to TaskFlow via SSO
  4. Observe the redirect loop: TaskFlow → Entra ID (success) → TaskFlow callback → redirect back to Entra ID

## Environment
TaskFlow with Microsoft Entra ID SSO (migrated from Okta). Multiple browsers affected (Chrome, Edge, Firefox). Not OS-dependent.

## Severity: high

## Impact
~30% of the team cannot reliably log in to TaskFlow. No consistent workaround exists. This blocks daily work for affected users.

## Recommended Fix
1. Inspect the SSO callback handler to see how it matches the identity token's email claim to stored user records. 2. Compare the email claim Entra ID returns (inspect the actual token) against what TaskFlow has stored — look for plus-tag normalization differences and primary-vs-alias mismatches. 3. Implement case-insensitive, plus-tag-normalized email matching, or match on a stable claim like the OIDC 'sub' or an immutable user ID rather than email. 4. Add a migration step or lookup table that maps old Okta email values to new Entra ID identities for alias cases. 5. Ensure old Okta session cookies are invalidated and don't interfere with the new flow.

## Proposed Test Case
Create test users with plus-addressed emails and with emails that differ from the token's email claim. Verify that SSO login succeeds for all variants, a valid session is created, and no redirect loop occurs. Also verify that stale session cookies from a previous IdP configuration do not cause a redirect loop.

## Information Gaps
- Exact email claim name and value Entra ID returns in the token (email, preferred_username, or UPN) versus what TaskFlow expects
- Whether TaskFlow uses OIDC or SAML with Entra ID
- Database schema for user identity storage — whether there is a secondary lookup field beyond email
