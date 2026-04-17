# Triage Summary

**Title:** SSO redirect loop after Okta-to-Entra ID migration for users with plus-addressed or aliased emails

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Microsoft but are immediately redirected back to the login flow upon returning to TaskFlow. The affected users appear to share a pattern: they either use plus-addressed email addresses (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during the migration.

## Root Cause Hypothesis
TaskFlow's SSO callback handler likely performs an email-based user lookup to match the authenticated identity to a local account. The email claim returned by Entra ID (likely the primary email or UPN) does not match the email stored in TaskFlow's user record for affected users. Plus-addressed emails may be normalized differently by Entra ID than by Okta (e.g., Entra strips the +tag or returns the UPN instead), and aliased users may have a different primary email in Entra ID than what TaskFlow has on file. When the lookup fails, TaskFlow treats the user as unauthenticated and restarts the login flow, causing the loop.

## Reproduction Steps
  1. Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO
  2. Create a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com)
  3. Configure the corresponding Entra ID account for that user
  4. Attempt to log in to TaskFlow via SSO
  5. Observe: after successful Microsoft authentication, the user is redirected back to TaskFlow and immediately sent back to Microsoft in a loop

## Environment
TaskFlow v2.3.1, self-hosted. Mixed browsers (Chrome, Edge, Firefox) and OS (Windows 10/11, macOS). SSO provider: Microsoft Entra ID (recently migrated from Okta).

## Severity: high

## Impact
Approximately 30% of the team is completely unable to log into TaskFlow. This is a total access blocker for affected users with no workaround mentioned.

## Recommended Fix
Investigate the SSO callback handler's user-matching logic. Compare the email claim returned by Entra ID (check the id_token or userinfo response) against the email stored in TaskFlow's user table for affected users. Likely fixes include: (1) normalizing plus-addressed emails before lookup (stripping the +tag), (2) matching on a stable identifier like the OIDC 'sub' claim rather than email, or (3) supporting multiple email aliases per user account. Also check whether Entra ID returns UPN vs. mail claim and ensure TaskFlow checks the correct one.

## Proposed Test Case
Create test users with plus-addressed emails and with email mismatches between the OIDC provider's claim and TaskFlow's stored email. Verify that SSO login completes successfully for these users without entering a redirect loop, and that they are matched to the correct TaskFlow account.

## Information Gaps
- No server-side logs or browser network traces from the redirect loop to confirm the exact failure point
- The exact OIDC claim (email, preferred_username, UPN) that TaskFlow uses for user matching is unknown
- Whether the aliased-email users have their Entra ID primary email matching their TaskFlow account email has not been confirmed
