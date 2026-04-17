# Triage Summary

**Title:** Login redirect loop caused by email mismatch between TaskFlow user records and Entra ID email claim

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop during login. They authenticate successfully with Entra but TaskFlow never establishes a session, sending them back to Entra immediately. Affected users fall into two groups: (1) users with plus-sign email addresses (e.g., jane+taskflow@company.com) where Entra strips the plus portion, and (2) users whose email addresses changed during the migration and now have aliases.

## Root Cause Hypothesis
TaskFlow's auth callback uses the email claim from the OIDC token to look up the corresponding local user account. Entra ID returns the user's primary email (e.g., jane@company.com), but TaskFlow has the user stored under a different email (e.g., jane+taskflow@company.com or a pre-migration address). The lookup fails, no session is created (explaining the cookie that doesn't stick), and TaskFlow's auth middleware sees an unauthenticated request and redirects back to the IdP, creating the loop.

## Reproduction Steps
  1. Ensure a TaskFlow user account exists with a plus-sign email (e.g., jane+taskflow@company.com) or a legacy email address that differs from their Entra ID primary email
  2. Configure TaskFlow SSO to use Microsoft Entra ID where the user's primary email is jane@company.com (no plus portion)
  3. Attempt to log in as that user
  4. Observe: successful Entra authentication, redirect to TaskFlow callback, immediate redirect back to Entra, infinite loop

## Environment
TaskFlow with SSO via Microsoft Entra ID (recently migrated from Okta). Affects users whose TaskFlow-stored email differs from their Entra ID primary email claim. Other Entra-integrated apps work fine for these users.

## Severity: high

## Impact
Approximately 30% of the team is completely locked out of TaskFlow with no known workaround. These users cannot access the application at all.

## Recommended Fix
Investigate the user lookup logic in TaskFlow's OIDC/OAuth callback handler. The fix likely involves one or more of: (1) Normalizing email comparison — strip plus-addressing before lookup, or use case-insensitive/alias-aware matching. (2) Switching the identity claim from `email` to a stable identifier like `sub` (OIDC subject) or `oid` (Entra object ID) and storing that mapping during migration. (3) As an immediate remediation, provide an admin tool or script to bulk-update TaskFlow user emails to match their Entra ID primary emails. Also verify that the auth callback handles lookup failures gracefully (return an error page rather than silently redirecting to login).

## Proposed Test Case
Create a test user in TaskFlow with email 'testuser+alias@company.com'. Configure the OIDC mock/test IdP to return 'testuser@company.com' as the email claim. Verify that the user can authenticate successfully and a session is created. Additionally, test with a user whose stored email is a completely different alias (pre-migration address) to ensure the lookup still resolves correctly.

## Information Gaps
- Exact OIDC claim TaskFlow uses for user lookup (likely `email` but unconfirmed — reporter needs to check with the original developer)
- Whether the changed-email/alias users exhibit the exact same mismatch pattern (reporter strongly suspects so but only verified one plus-address case)
- Whether TaskFlow logs an explicit error during the failed lookup or fails silently
