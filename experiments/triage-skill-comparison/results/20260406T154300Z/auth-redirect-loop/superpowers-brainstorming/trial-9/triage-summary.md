# Triage Summary

**Title:** SSO redirect loop for users whose email differs between Entra ID and TaskFlow user record

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully with Entra ID but TaskFlow fails to establish a session, sending them back to the identity provider repeatedly. Affected users are those with plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses changed during the migration. Users whose primary email is identical in both systems log in without issue.

## Root Cause Hypothesis
TaskFlow's SSO callback handler matches the authenticated user by comparing the email claim in the Entra ID token against the email stored in its local user database. When these don't match exactly — because the user's Entra ID primary email differs from the plus-addressed or legacy email stored in TaskFlow from the Okta era — the lookup fails. Without a matched user, no session is created, and the app's auth middleware redirects back to the login flow, creating the loop. The intermittent success with incognito/cleared cookies suggests stale Okta session cookies may sometimes interfere as well, but the primary cause is the email mismatch.

## Reproduction Steps
  1. Identify a user whose email in TaskFlow's user table differs from their Entra ID primary email (e.g., a plus-addressed email or a pre-migration alias)
  2. Have that user attempt to log in via SSO
  3. Observe: Entra ID authentication succeeds, redirect back to TaskFlow occurs, but TaskFlow immediately redirects to Entra ID again
  4. Confirm the loop continues indefinitely until the browser is closed or cookies are cleared

## Environment
TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects users with email mismatches between the two identity providers.

## Severity: high

## Impact
Approximately 30% of users are locked out of TaskFlow entirely. Current workaround (clearing cookies / incognito) is unreliable and not sustainable.

## Recommended Fix
1. Inspect the SSO callback handler's user-lookup logic — confirm it matches on the email claim from the ID token. 2. Normalize the comparison: either case-insensitive match, or strip plus-address suffixes, or match on a stable identifier (e.g., Entra ID `oid` or `sub` claim) instead of email. 3. Build a one-time migration script to reconcile TaskFlow user records with Entra ID identities — update stored emails or add an `external_id` / `oid` column mapped to the Entra ID object ID. 4. Add logging in the SSO callback so failed user lookups are recorded with the attempted email, rather than silently redirecting. 5. Clear or invalidate any residual Okta session cookies by rotating the session secret or adjusting cookie domain/name.

## Proposed Test Case
Create a test user in TaskFlow whose stored email is a plus-addressed variant (user+tag@company.com) while the Entra ID token returns the base email (user@company.com). Attempt SSO login and verify the user is matched, a session is created, and no redirect loop occurs. Additionally, test with a user whose email was changed during migration (old alias vs new primary) to confirm the same fix handles both cases.

## Information Gaps
- Exact field name used for user lookup in the SSO callback (email, UPN, or sub claim)
- Whether TaskFlow stores an external identity provider ID or relies solely on email matching
- Whether the Entra ID token returns the UPN, mail, or preferred_username claim — and which one TaskFlow reads
