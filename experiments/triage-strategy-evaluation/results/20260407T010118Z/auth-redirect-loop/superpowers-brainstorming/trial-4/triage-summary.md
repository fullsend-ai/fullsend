# Triage Summary

**Title:** Login redirect loop: email claim mismatch between Entra ID tokens and TaskFlow user records

## Problem
After migrating SSO from Okta to Microsoft Entra ID, ~30% of users experience an infinite redirect loop during login. They authenticate successfully with Entra ID but TaskFlow fails to match them to a user record, invalidates the session, and redirects back to the IdP. The affected users fall into two categories: those with plus-addressed emails (jane+taskflow@company.com) and those whose email was changed or aliased during the Okta era.

## Root Cause Hypothesis
TaskFlow matches the incoming SSO token's email claim against its stored user records. Entra ID normalizes or strips plus-addressed email tags (sending `jane@company.com` instead of `jane+taskflow@company.com`), or sends a different claim attribute (e.g., UPN vs. email) than Okta did. For aliased users, Entra ID sends their current canonical email while TaskFlow still stores the old one. In both cases, the lookup fails, TaskFlow cannot establish a session, and the user is redirected back to the IdP in a loop.

## Reproduction Steps
  1. Identify a user whose TaskFlow account email is a plus-addressed form (e.g., jane+taskflow@company.com)
  2. Have that user attempt to log into TaskFlow via Microsoft Entra ID SSO
  3. Observe successful Entra ID authentication followed by redirect back to TaskFlow
  4. TaskFlow fails to match the email claim to a user record and redirects back to Entra ID
  5. The cycle repeats indefinitely

## Environment
TaskFlow instance with SSO recently migrated from Okta to Microsoft Entra ID. Affected users have plus-addressed emails or historically changed/aliased emails in TaskFlow's user store. Issue is browser-independent (reproduced on Chrome and Edge).

## Severity: high

## Impact
~30% of the team cannot log into TaskFlow at all. Complete loss of access for affected users with no reliable workaround (incognito works intermittently).

## Recommended Fix
1. Inspect the Entra ID token claims for an affected user (decode the id_token or check server logs) to confirm which email value Entra ID is actually sending and which claim attribute it uses (email, preferred_username, UPN). 2. Compare that against TaskFlow's user lookup logic. 3. Fix the lookup to normalize email comparison — either strip plus tags before matching, or match on a canonical identifier. 4. For aliased users, add a migration step or secondary-email lookup to match old stored emails to current Entra ID identities. 5. Consider adding a user-mapping table or matching on a stable IdP subject identifier (sub/oid) rather than email, which is inherently mutable.

## Proposed Test Case
Create test users with plus-addressed emails (user+tag@domain.com) and with an email that differs from the IdP claim. Verify that SSO login successfully matches these users to their TaskFlow accounts and establishes a session without redirect loops.

## Information Gaps
- Exact claim attribute and value Entra ID sends in the token (developer can decode a token or check auth logs to confirm)
- Whether TaskFlow's auth code has explicit email normalization logic or relies on exact string matching (code inspection needed)
- Why incognito occasionally works — likely a stale session cookie from a prior failed auth attempt causes the loop to persist even after a correct match; incognito avoids this. Developer should verify session cleanup on auth failure.
