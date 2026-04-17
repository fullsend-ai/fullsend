# Triage Summary

**Title:** Login redirect loop for users whose stored email doesn't match Entra ID email claim (plus-addressing / alias mismatch)

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite login redirect loop. These users authenticate successfully with Entra but are immediately redirected back to the IdP instead of landing in TaskFlow. Affected users are those whose TaskFlow account email (e.g. jane+taskflow@company.com) doesn't match the email claim Entra ID sends (e.g. jane@company.com) — typically users who used plus-addressing or had email aliases configured under Okta.

## Root Cause Hypothesis
TaskFlow's auth callback uses the `email` claim from the OIDC token to look up the internal user record. When Entra sends `jane@company.com` but TaskFlow has `jane+taskflow@company.com` stored, the lookup fails. Instead of returning an explicit 'account not found' error, the callback fails silently to create a valid session — it issues a Set-Cookie header, but the session is empty or invalid. On the next request, the auth middleware detects no valid session and redirects back to Entra, creating the loop. Okta was likely configured to pass the plus-addressed email (or a different claim like UPN) that matched TaskFlow's records, masking this strict-match behavior.

## Reproduction Steps
  1. Ensure a TaskFlow user account exists with a plus-addressed or aliased email (e.g. jane+taskflow@company.com)
  2. Configure Entra ID as the SSO provider, where that user's primary email is the base address (jane@company.com)
  3. Attempt to log in as that user
  4. Observe: Entra authentication succeeds, redirect back to TaskFlow occurs, but user is immediately redirected back to Entra in a loop
  5. Check browser dev tools: a Set-Cookie header is present on the callback response, but subsequent requests show no valid session cookie being sent

## Environment
TaskFlow with OIDC SSO using the `email` claim for user matching; recently migrated from Okta to Microsoft Entra ID; affects users with plus-addressing or email aliases where Okta-stored email differs from Entra primary email

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all. No reliable workaround exists — clearing cookies and incognito mode work intermittently but not consistently. This is a complete access blocker for affected users.

## Recommended Fix
Two-pronged approach: (1) **Immediate fix — data migration:** Write a script to update TaskFlow's stored user emails to match what Entra ID sends as the `email` claim. For plus-addressed users, update `jane+taskflow@company.com` → `jane@company.com`. Cross-reference Entra ID sign-in logs or user export to get the authoritative email for each user. (2) **Defensive fix — matching logic:** Update TaskFlow's user lookup in the auth callback to normalize emails before matching (strip plus-addressing, case-insensitive comparison) OR support matching on multiple claims (`email`, `preferred_username`, `sub`). Also add an explicit error page when user lookup fails instead of silently failing session creation — this prevents redirect loops from any future identity mismatches. (3) **Optional Entra-side fix:** Configure a claims transformation policy in Entra ID to emit the plus-addressed email if that's the desired canonical form, though fixing TaskFlow's matching logic is more robust.

## Proposed Test Case
Create a test user in TaskFlow with email `testuser+tag@company.com`. Configure the OIDC mock/test IdP to return `testuser@company.com` as the email claim. Verify: (a) with current code, login results in redirect loop or explicit error (not silent failure), (b) after fix, login succeeds and maps to the correct TaskFlow account. Additionally, test that a completely unknown email produces a clear 'account not found' error page, not a redirect loop.

## Information Gaps
- Exact auth callback code path — whether it explicitly handles 'user not found' or falls through silently (suspected: falls through)
- Whether TaskFlow stores any secondary identifiers (user ID, UPN) that could be used as fallback matching
- Whether the intermittent incognito success is due to a race condition, cached DNS, or some other factor — low priority to investigate since the root cause is clear
- Whether any affected users have a pattern beyond plus-addressing (e.g. case sensitivity differences in email)
