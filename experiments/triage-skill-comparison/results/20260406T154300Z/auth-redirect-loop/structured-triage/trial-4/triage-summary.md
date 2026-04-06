# Triage Summary

**Title:** SSO login redirect loop for users with plus-addressed or migrated email addresses after Okta-to-Entra ID switch

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Microsoft but are immediately redirected back to the login flow upon returning to TaskFlow. The affected users are those with plus-addressed emails (e.g., jane+taskflow@company.com) or users whose email addresses were changed/aliased during the migration.

## Root Cause Hypothesis
TaskFlow is likely matching the authenticated user's identity by comparing the email claim from the SSO token against stored user records. Plus-addressed emails or migrated/aliased addresses may not match exactly — either because TaskFlow is not normalizing plus-addressing (stripping the +tag portion), or because the email claim returned by Entra ID differs from what Okta returned (e.g., Entra sends the primary alias while the stored record has the old or plus-addressed form). When the lookup fails, TaskFlow treats the user as unauthenticated and restarts the login flow, causing the loop.

## Reproduction Steps
  1. Set up TaskFlow 2.3.1 (self-hosted) with Microsoft Entra ID SSO
  2. Create or identify a user account whose email in TaskFlow uses plus-addressing (e.g., user+taskflow@company.com) or whose email was changed/aliased during the Okta-to-Entra migration
  3. Attempt to log in as that user
  4. Authenticate successfully in the Microsoft login page
  5. Observe that upon redirect back to TaskFlow, the user is immediately sent back to Microsoft in a loop

## Environment
TaskFlow 2.3.1, self-hosted. Windows 10 and 11. Multiple browsers (Chrome, Edge, Firefox). SSO provider: Microsoft Entra ID (recently migrated from Okta).

## Severity: high

## Impact
~30% of the company's users are completely unable to log into TaskFlow. This blocks all their work in the application. The affected population cuts across all roles and permission levels.

## Recommended Fix
Investigate the SSO callback handler's user-matching logic. Check how the email claim from the Entra ID token is compared against stored user records. Likely fixes include: (1) normalizing plus-addressed emails before comparison (stripping the +tag), (2) performing case-insensitive comparison, (3) checking against all known email aliases for a user rather than a single stored address, or (4) adding a migration step that reconciles stored email addresses with the canonical addresses returned by Entra ID. Also check whether the email claim name differs between Okta and Entra ID tokens (e.g., 'email' vs 'preferred_username' vs 'upn').

## Proposed Test Case
Create test users with plus-addressed emails and with emails that differ between the stored record and the SSO token claim. Verify that SSO login completes successfully without looping for these users, and that the correct user session is established after authentication.

## Information Gaps
- No server-side logs or browser network traces from an affected login attempt — these could confirm the exact point of failure in the redirect flow
- The exact email claim being returned by Entra ID tokens vs. what is stored in TaskFlow's user database has not been compared directly
