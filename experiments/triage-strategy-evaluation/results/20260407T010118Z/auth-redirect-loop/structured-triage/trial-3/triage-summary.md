# Triage Summary

**Title:** SSO login redirect loop after Okta-to-Entra migration for users with plus-addresses or changed emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID (OIDC), approximately 30% of users experience an infinite redirect loop when logging in. They authenticate successfully with Entra but TaskFlow fails to match their session and redirects them back to the IdP. The issue correlates with users who have plus-format email addresses (e.g., jane+taskflow@company.com) or whose email addresses were changed in the directory at some point. Clearing cookies or using incognito mode sometimes resolves it temporarily.

## Root Cause Hypothesis
TaskFlow is likely matching the OIDC identity token's email or subject claim against a stored user record, and the match fails for affected users. Entra ID may normalize or strip the plus-address suffix (returning jane@company.com instead of jane+taskflow@company.com), or it may return a different email claim than Okta did for users whose emails were changed. When the lookup fails, TaskFlow doesn't create a session and redirects back to the IdP, creating the loop. The intermittent cookie-clearing fix suggests stale Okta session cookies may also interfere in some cases.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 (self-hosted) with OIDC against Microsoft Entra ID
  2. Ensure a user account exists in TaskFlow with a plus-format email (e.g., jane+taskflow@company.com)
  3. Have that user attempt to log in via SSO
  4. Observe: user authenticates with Entra successfully, is redirected back to TaskFlow, and is immediately redirected to Entra again in a loop

## Environment
TaskFlow v2.3.1, self-hosted, OIDC protocol, Microsoft Entra ID as IdP (migrated from Okta). Observed on Chrome, Edge, and Firefox.

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all. These users are completely blocked from using the application. Workarounds (incognito mode, clearing cookies) are inconsistent.

## Recommended Fix
1. Check how TaskFlow resolves the OIDC identity token to a local user record — inspect whether it matches on the `email` claim, the `sub` claim, or another field. 2. Compare the email/sub values in the Entra ID token against what's stored in TaskFlow's user table for affected users (look for plus-address stripping or email mismatches). 3. If matching on `email`, consider switching to the stable `sub` (subject) claim, or add email normalization logic that handles plus-addressing. 4. For users with changed emails, ensure the stored identifier matches what Entra returns. 5. Investigate whether stale Okta session cookies are interfering — consider invalidating all existing sessions post-migration.

## Proposed Test Case
Create test users with plus-format emails and users whose emails have been updated. Perform OIDC login flow and verify that the identity token's claims correctly resolve to the right local user record without triggering a redirect loop. Additionally, test login with pre-existing session cookies from the old IdP to confirm they're handled gracefully.

## Information Gaps
- Server-side auth logs showing the exact failure point in the redirect loop (reporter is willing to check but unsure where to find them)
- The exact OIDC claims (email, sub, preferred_username) that Entra ID returns for affected vs. unaffected users
- Whether TaskFlow's user lookup uses the email claim, sub claim, or another identifier
