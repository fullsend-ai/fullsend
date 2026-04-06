# Triage Summary

**Title:** SSO login redirect loop for users with plus-addressed or migrated email addresses after Okta-to-Entra ID switch

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Microsoft succeeds, but TaskFlow fails to establish a session — the Set-Cookie header is present in the response but the cookie does not persist on subsequent requests, causing the app to redirect back to the IdP indefinitely.

## Root Cause Hypothesis
TaskFlow's session establishment logic likely compares the email claim in the OIDC token from Entra ID against the user's stored email in the TaskFlow database. For users with plus-addressed emails (e.g., jane+taskflow@company.com) or emails that were changed/aliased during the Entra ID migration, the email claim returned by Entra ID does not match what TaskFlow expects. The session creation fails silently — the app either never writes a valid session cookie or immediately invalidates it — causing the redirect loop. Users with unchanged, plain email addresses match correctly and log in fine. The incognito workaround occasionally working suggests that stale cookies or cached auth state from the old Okta integration may also interfere with the flow.

## Reproduction Steps
  1. Identify or create a test user in Entra ID with a plus-addressed email (e.g., testuser+taskflow@company.com)
  2. Ensure this user exists in TaskFlow's user database (possibly under a different email variant)
  3. Attempt to log in to TaskFlow via SSO
  4. Observe the redirect loop: Microsoft auth succeeds, redirect to TaskFlow occurs, but the session cookie does not persist and the user is immediately redirected back to Microsoft
  5. Compare the email claim in the OIDC token (visible in browser dev tools Network tab) with the email stored in TaskFlow's user record

## Environment
TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects users in Chrome (and likely all browsers). Entra ID app registration and redirect URIs confirmed correct. Affected users can authenticate with other Entra ID apps successfully.

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all. No reliable workaround exists (incognito is intermittent). This blocks affected users entirely from using the application.

## Recommended Fix
1. Inspect TaskFlow's SSO callback handler — find where it matches the incoming OIDC email claim against stored user records. 2. Check whether the comparison is exact-match or normalized. Plus-addressing (RFC 5233) and email aliasing likely cause mismatches. 3. Implement email normalization: strip plus-address suffixes and/or perform case-insensitive comparison when matching token claims to user records. 4. Alternatively, match on a stable claim like `sub` (subject identifier) or `oid` (object ID) instead of email, which is more robust across identity provider migrations. 5. Clear any stale Okta session cookies that may still be present in users' browsers (set explicit expiry/deletion on the old cookie names). 6. Add logging to the session creation path so silent failures surface as errors rather than redirect loops.

## Proposed Test Case
Write an integration test that performs SSO login with a user whose OIDC email claim contains a plus-address suffix (user+tag@domain.com) while the TaskFlow database stores the base address (user@domain.com). Verify that the session is established successfully and the user is not redirected back to the IdP. Add a second test case for a user whose email was aliased (old@domain.com in DB, new@domain.com in token) to verify matching by subject identifier works.

## Information Gaps
- Exact field TaskFlow uses to match OIDC token claims to user records (email vs. sub vs. UPN)
- Whether TaskFlow's session middleware logs the reason for session creation failure
- Whether the stale Okta cookies are actively interfering or are merely present
