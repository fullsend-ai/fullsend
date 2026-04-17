# Triage Summary

**Title:** SSO redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra ID migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. They authenticate successfully with Entra ID but TaskFlow cannot match them to an existing account, so it sends them back to the identity provider. The affected users are those whose TaskFlow account email differs from the canonical email Entra ID returns — either because they used plus addressing (jane+taskflow@company.com) or originally registered with a personal email that was later aliased to a corporate address in Entra ID.

## Root Cause Hypothesis
TaskFlow's SSO callback handler performs an exact email match between the email claim in the OIDC/SAML token from the identity provider and the email stored in its user database. Okta was likely configured to pass through the plus-addressed or aliased email as-is, but Entra ID returns the canonical/primary email (jane@company.com). The mismatch causes the user-lookup to fail, and TaskFlow treats this as an unauthenticated request, restarting the login flow and creating the redirect loop. The inconsistent incognito success may be due to stale Okta session cookies or cached OIDC state interfering with the flow in regular browser sessions.

## Reproduction Steps
  1. Ensure a TaskFlow user account exists with a plus-addressed email (e.g., jane+taskflow@company.com) or a personal email that is aliased in Entra ID
  2. Attempt to log into TaskFlow via SSO
  3. Authenticate successfully with Entra ID using the canonical company email
  4. Observe that TaskFlow redirects back to the Entra ID login page in a loop instead of completing authentication

## Environment
TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Affects all browsers (Chrome, Edge, Firefox). Reproducible in both regular and incognito sessions, though incognito occasionally succeeds.

## Severity: high

## Impact
Approximately 30% of the team is completely locked out of TaskFlow. These users cannot work around the issue reliably. Productivity loss across the affected portion of the organization.

## Recommended Fix
Investigate the SSO callback handler's user-lookup logic. Instead of exact email matching, normalize emails before comparison (strip plus-address suffixes, canonicalize to lowercase). Additionally, consider matching on a stable OIDC subject identifier (the 'sub' claim) rather than email, since email addresses can vary across identity providers. For the aliased-email users, either update TaskFlow's stored email to match the Entra ID primary email, or implement a lookup that checks all known aliases. As an immediate workaround, affected users' emails in the TaskFlow database could be updated to match their canonical Entra ID email.

## Proposed Test Case
Create test users with (a) a plus-addressed email, (b) a personal email aliased to a corporate email in the IdP, and (c) a plain matching email. Mock the OIDC callback with tokens returning the canonical corporate email for all three. Verify that all three users are correctly matched and authenticated without redirect loops.

## Information Gaps
- Exact OIDC/SAML claim TaskFlow uses for user matching (email, preferred_username, sub, etc.)
- Whether TaskFlow logs the failed user-lookup or just silently redirects
- Whether the Entra ID token includes the plus-addressed email anywhere (e.g., in a secondary claim or custom attribute)
