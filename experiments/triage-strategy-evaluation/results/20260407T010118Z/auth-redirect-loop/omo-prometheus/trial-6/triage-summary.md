# Triage Summary

**Title:** Login redirect loop due to email claim mismatch after Okta-to-Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite login redirect loop. Users authenticate successfully with Entra but TaskFlow cannot match them to an existing account because the email address in Entra's email claim differs from what TaskFlow has stored from the Okta era.

## Root Cause Hypothesis
TaskFlow performs user lookup using the `email` claim from the OIDC token. Two categories of users have mismatches: (1) users with plus-addressed emails (e.g., TaskFlow stores `jane+taskflow@company.com` but Entra sends `jane@company.com` as the primary email), and (2) users whose primary email changed during migration (e.g., TaskFlow stores `mike.s@company.com` but Entra sends `michael.smith@company.com`). When lookup fails, TaskFlow does not establish a valid session and redirects back to the IdP, creating the loop.

## Reproduction Steps
  1. Identify a user whose email in TaskFlow's database differs from their primary email in Entra ID (e.g., a plus-addressed user or one whose email changed during migration)
  2. Have that user attempt to log into TaskFlow via SSO
  3. User authenticates successfully with Microsoft Entra ID
  4. Entra redirects back to TaskFlow with an id_token containing the Entra primary email
  5. TaskFlow attempts user lookup by email claim, finds no match, fails to create a session
  6. TaskFlow redirects the user back to Entra to re-authenticate, creating an infinite loop

## Environment
TaskFlow with OIDC/SSO integration, recently migrated from Okta to Microsoft Entra ID. User matching is configured on the `email` claim. Affects ~30% of users — those with plus-addressed emails or emails that changed during migration.

## Severity: high

## Impact
~30% of the team is effectively locked out of TaskFlow with no reliable workaround. Clearing cookies works intermittently but the loop returns. This is a blocking issue for daily work for affected users.

## Recommended Fix
Two-pronged approach: (1) Immediate: Update affected users' email addresses in TaskFlow's database to match what Entra ID sends in the email claim — run a reconciliation script comparing TaskFlow stored emails against Entra directory entries. (2) Longer-term: Modify TaskFlow's user lookup to either match on a stable identifier like the OIDC `sub` claim instead of email, or support email alias mapping so that multiple email addresses can resolve to the same user. Additionally, add explicit error handling for the 'user not found after successful SSO' case — instead of silently restarting the auth flow, display an error message like 'Your SSO account could not be matched to a TaskFlow user. Contact your administrator.'

## Proposed Test Case
Create a test user in TaskFlow with email `testuser+alias@company.com`. Configure the test IdP to return `testuser@company.com` as the email claim. Attempt login and verify that (a) after the fix, the user is matched correctly and logged in, and (b) if no match is found, a clear error is shown instead of a redirect loop.

## Information Gaps
- Exact TaskFlow SSO configuration field name for email claim mapping (reporter said 'pretty sure' it's email-based but hasn't confirmed the config)
- Whether the intermittent cookie-clearing success is from residual Okta sessions or another mechanism
- Whether Conditional Access policies differ across user groups (reporter believes they're uniform but hasn't verified)
