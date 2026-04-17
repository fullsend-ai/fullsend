# Triage Summary

**Title:** SSO redirect loop for users with plus-addressed or migrated email addresses after Okta-to-Entra ID switch

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but TaskFlow fails to establish a session, sending them back to the identity provider. The affected users share a common trait: they use plus-addressed emails (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during the migration.

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches the email claim from the identity provider against stored user records. Entra ID is likely returning the canonical email address (jane@company.com) in its claims, while TaskFlow's user records still contain the plus-addressed variant (jane+taskflow@company.com) or a pre-migration alias. The match fails, so TaskFlow cannot identify the user, does not create a session, and redirects back to login — creating the loop. The intermittent success in incognito windows suggests that stale Okta session cookies or cached tokens may also interfere with the new Entra ID flow in some cases.

## Reproduction Steps
  1. Set up a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com)
  2. Configure Entra ID with the canonical email (user@company.com) for the same person
  3. Attempt to log into TaskFlow via SSO
  4. Observe successful Entra ID authentication followed by redirect back to login, looping indefinitely

## Environment
TaskFlow with Microsoft Entra ID SSO integration (recently migrated from Okta). Occurs across all browsers (Chrome, Edge, Firefox). Affects users with plus-addressed emails or emails that changed during migration.

## Severity: high

## Impact
~30% of the team cannot reliably log into TaskFlow. These users are effectively locked out of the application, with only intermittent access via incognito as a fragile workaround.

## Recommended Fix
1. Inspect the SSO callback handler's user-lookup logic — confirm how it matches the incoming email claim against stored user records. 2. Normalize email comparison: strip plus-addressing (everything between + and @) before matching, or match on a canonical email field. 3. For migrated/aliased users, add a migration step or lookup that maps old email variants to the current Entra ID email. 4. Clear or invalidate any legacy Okta session cookies/tokens that may conflict with the new Entra ID flow. 5. Consider storing the identity provider's subject identifier (sub/oid claim) as the primary lookup key rather than email, to avoid future email-mismatch issues.

## Proposed Test Case
Create test users with plus-addressed emails and email aliases. Simulate SSO login where the IdP returns the canonical (non-plus-addressed) email in claims. Verify that TaskFlow correctly resolves the user, creates a session, and does not redirect back to the IdP. Also verify that stale session cookies from a previous IdP do not interfere with the new login flow.

## Information Gaps
- Which specific claim field TaskFlow uses for user matching (email, preferred_username, upn, sub/oid)
- Whether TaskFlow's SSO implementation uses OIDC or SAML with Entra ID
- Exact server-side logs or error messages during the failed redirect cycle
- Whether the intermittent incognito success correlates with cookie state or is truly random
