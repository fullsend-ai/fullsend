# Triage Summary

**Title:** SSO login redirect loop due to email normalization mismatch between Entra ID and TaskFlow

## Problem
After migrating from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. Users authenticate successfully with Microsoft but are immediately redirected back to the login page repeatedly. The issue correlates with users who have plus-sign email addresses (e.g., jane+taskflow@company.com) or emails that were changed/aliased during the SSO migration.

## Root Cause Hypothesis
Entra ID normalizes or strips plus-sign email addresses when returning claims (e.g., returning jane@company.com instead of jane+taskflow@company.com). TaskFlow's user lookup compares the SSO-returned email against its stored email using an exact match. When they differ, the lookup fails, no session is created, and the user is redirected back to the SSO provider, creating the loop. Okta likely preserved the full plus-sign address in its claims, masking this mismatch before the migration.

## Reproduction Steps
  1. Set up a TaskFlow user account with a plus-sign email address (e.g., testuser+taskflow@company.com)
  2. Configure Entra ID SSO for TaskFlow
  3. Attempt to log in as that user via SSO
  4. Observe: authentication succeeds at Microsoft, but TaskFlow redirects back to Microsoft in a loop
  5. Check server logs to confirm the email returned by Entra ID differs from the stored email

## Environment
TaskFlow v2.3.1, self-hosted. Windows 10/11 and macOS. Reproduced on Chrome, Edge, and Firefox. SSO provider: Microsoft Entra ID (recently migrated from Okta).

## Severity: high

## Impact
Approximately 30% of the team is completely unable to log in to TaskFlow. These users are locked out of the application entirely, with no reliable workaround (incognito windows work intermittently).

## Recommended Fix
Investigate the SSO callback handler where the email claim from Entra ID is matched against stored user records. Likely fix: normalize emails before comparison (strip plus-sign suffixes, case-fold, or match on the base address). Alternatively, add a secondary lookup by alias or external SSO identifier (sub/oid claim) rather than relying solely on email matching. Also check whether the session creation code is failing silently on the lookup miss instead of returning an auth error, which is what causes the loop rather than a clear error page.

## Proposed Test Case
Create a user with a plus-sign email (user+tag@example.com). Simulate an SSO callback where the identity provider returns the plain email (user@example.com). Verify that the user is matched correctly and a session is created. Also test with aliased/changed emails to ensure the lookup is resilient to common email variations.

## Information Gaps
- Exact log entries showing the email mismatch and session creation failure (reporter confirmed seeing this but hasn't provided verbatim logs)
- Whether all affected users have plus-sign or aliased emails, or if there are other patterns
- Which Entra ID claim TaskFlow is using for email matching (email, preferred_username, or UPN)
- Whether the incognito workaround correlates with cached stale session cookies from the old Okta integration
