# Triage Summary

**Title:** SSO redirect loop caused by email claim mismatch after Okta-to-Entra migration

## Problem
After migrating from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but are immediately redirected back to the IdP upon returning to TaskFlow. The affected population correlates strongly with users whose TaskFlow account email differs from the email claim Entra ID returns — specifically users with plus-addressed emails (e.g., jane+taskflow@company.com) and users whose email addresses were changed or aliased during the migration.

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches the incoming identity token's email claim against stored user records to establish a session. Entra ID likely returns the canonical/primary email (jane@company.com) rather than the plus-addressed variant (jane+taskflow@company.com), or returns the new consolidated email for migrated/aliased users. When no matching account is found, TaskFlow fails to create an authenticated session and redirects back to the IdP, creating the loop. The intermittent success with cleared cookies or incognito windows may be due to the absence of a stale pre-migration session cookie that interferes with the new auth flow on some attempts.

## Reproduction Steps
  1. Have a user create a TaskFlow account with a plus-addressed email (e.g., user+taskflow@company.com) registered in Entra ID
  2. Attempt to log in via SSO
  3. Observe that Entra ID authentication succeeds (user is redirected back to TaskFlow)
  4. Observe that TaskFlow immediately redirects back to Entra ID, creating an infinite loop
  5. Repeat with a user whose email matches the Entra ID primary email exactly — login succeeds

## Environment
TaskFlow with SSO integration, recently migrated from Okta to Microsoft Entra ID. Entra ID app registration and redirect URIs confirmed correct. Issue is identity-provider-agnostic in root cause (email matching logic) but surfaced by the provider change.

## Severity: high

## Impact
Approximately 30% of the team is locked out of TaskFlow entirely. These users cannot reliably log in — workarounds (clearing cookies, incognito) are inconsistent. This blocks their daily work with the task management system.

## Recommended Fix
1. Inspect the SSO callback handler's user-lookup logic. Confirm it matches on the email claim from the OIDC/SAML token against stored user emails. 2. Log the exact email claim Entra ID returns for affected users and compare it to their stored TaskFlow email. 3. Fix the matching: either normalize plus-addressed emails before lookup (strip the +tag portion), or match on a stable identifier like the OIDC `sub` claim or Entra Object ID instead of email. 4. For migrated/aliased users, add a one-time migration step or allow matching on any known alias. 5. Consider storing the IdP subject identifier alongside the email to prevent recurrence on future migrations.

## Proposed Test Case
Create test users with plus-addressed emails and with emails that differ from the IdP's primary email claim. Verify that SSO login resolves to the correct TaskFlow account in all cases without redirect loops. Also verify that a user whose email is updated in the IdP can still log into their existing TaskFlow account.

## Information Gaps
- Exact OIDC/SAML claim TaskFlow uses for identity matching (email, sub, preferred_username, etc.)
- Whether TaskFlow stores any IdP-specific subject identifier or relies solely on email
- Server-side logs from the SSO callback during a failed login attempt
