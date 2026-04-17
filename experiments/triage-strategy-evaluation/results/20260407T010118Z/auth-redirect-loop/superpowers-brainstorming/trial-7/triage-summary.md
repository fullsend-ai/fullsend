# Triage Summary

**Title:** Login redirect loop caused by email mismatch between Entra ID claims and TaskFlow user records after SSO migration

## Problem
After migrating SSO from Okta to Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but are redirected back to the login page repeatedly. Affected users are those whose email address in Entra ID does not exactly match the email stored in TaskFlow's user database — primarily users with plus-sign addressing (e.g., TaskFlow has jane+taskflow@company.com but Entra sends jane@company.com) and users whose email aliases were consolidated during migration.

## Root Cause Hypothesis
TaskFlow's SSO callback handler performs an exact string match between the email claim in the Entra ID authentication response and the email stored in the local user table. When there is no match, TaskFlow fails to find or create a session for the user and redirects them back to the login flow, creating an infinite loop. This was not an issue under Okta because Okta was likely configured to send the plus-addressed or aliased email that matched TaskFlow's records.

## Reproduction Steps
  1. Set up a user in TaskFlow with a plus-addressed email (e.g., testuser+taskflow@company.com)
  2. Configure the same user in Entra ID with their canonical email (testuser@company.com) as the primary/claim email
  3. Attempt to log into TaskFlow via SSO
  4. Observe infinite redirect loop between TaskFlow and Entra ID

## Environment
TaskFlow with Entra ID (Microsoft) SSO integration, migrated from Okta. SAML or OIDC-based authentication. Affects users with email mismatches between the IdP and TaskFlow's user store.

## Severity: high

## Impact
Approximately 30% of users are completely unable to log into TaskFlow. Workarounds (clearing cookies, incognito) are unreliable and temporary.

## Recommended Fix
1. Investigate the SSO callback handler where the incoming email claim is matched against the local user table. Look for exact string comparison on the email field. 2. Implement email normalization before comparison — at minimum, strip plus-addressing suffixes (everything between + and @) and do a case-insensitive match. 3. Alternatively or additionally, match on a more stable identifier than email (e.g., Entra ID object ID / sub claim) and store it in the user record. 4. As an immediate remediation, update affected users' email addresses in TaskFlow's database to match what Entra ID sends, or configure Entra ID to send the plus-addressed email in the claim.

## Proposed Test Case
Create users with emails containing plus-addressing (user+tag@domain.com) and aliases. Configure the IdP to return the canonical email without the plus suffix. Verify that SSO login succeeds and maps to the correct user account. Also verify that users with exactly matching emails continue to work.

## Information Gaps
- Whether TaskFlow uses SAML or OIDC for the Entra ID integration (does not change the fix approach, just which claim attribute to inspect)
- The exact reason incognito/cookie clearing sometimes temporarily resolves the issue — likely cached redirect responses or stale session cookies from the failed auth attempt, but not confirmed
- Whether there are affected users who do NOT fall into the plus-addressing or alias-mismatch categories
