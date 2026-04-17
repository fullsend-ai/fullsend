# Triage Summary

**Title:** Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra-ID SSO migration

## Problem
After switching SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft but are immediately redirected back to Microsoft upon returning to TaskFlow. The affected users disproportionately have plus-addressed emails (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during Entra ID setup.

## Root Cause Hypothesis
TaskFlow's session establishment logic compares the email claim in the incoming OIDC/SAML token against stored user records using an exact string match. Okta likely normalized or preserved plus-addressed emails in a way that matched TaskFlow's records, but Entra ID either strips the plus-address tag, lowercases differently, or returns the primary alias instead of the plus-addressed variant. When the lookup fails, TaskFlow cannot establish a session and redirects back to the IdP, creating the loop. The same issue affects users whose canonical email changed during the Entra ID migration — the token email no longer matches the stored email.

## Reproduction Steps
  1. Configure TaskFlow to use Microsoft Entra ID for SSO
  2. Create or ensure a user account in TaskFlow associated with a plus-addressed email (e.g., testuser+taskflow@company.com)
  3. Ensure the corresponding Entra ID account has the base email (testuser@company.com) or a different alias as its primary
  4. Attempt to log in as that user
  5. Observe the infinite redirect loop between TaskFlow and the Entra ID login page

## Environment
TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Affects users with plus-addressed emails or email aliases that differ between the identity provider and TaskFlow's user records.

## Severity: high

## Impact
~30% of the team is completely unable to log into TaskFlow. These users are fully blocked with no known workaround. Other SSO-dependent apps work fine for them, so the issue is specific to TaskFlow's token-to-user matching logic.

## Recommended Fix
Investigate the authentication callback handler where TaskFlow matches the incoming token's email claim to a stored user record. (1) Log the exact email claim value Entra ID sends vs. what TaskFlow has stored for affected users. (2) Implement email normalization: strip plus-address tags before comparison, apply case-insensitive matching, and consider matching against email aliases. (3) Provide an admin tool or migration script to reconcile user email records with Entra ID identities. (4) If TaskFlow caches the old Okta token format or issuer, ensure token validation accepts the new Entra ID issuer.

## Proposed Test Case
Unit test the user-lookup-by-email function with inputs: exact match, plus-addressed variant, different-case variant, and alias email. Verify that all variants resolve to the correct user record. Integration test: complete an SSO login flow with a plus-addressed email and confirm a session is established without redirect looping.

## Information Gaps
- Exact email claim values Entra ID is sending in tokens for affected users (server-side log inspection needed)
- Whether TaskFlow stores emails from the original Okta provisioning or from its own user registration
- Whether the app uses OIDC or SAML with Entra ID, which affects how email claims are formatted
