# Triage Summary

**Title:** Infinite login redirect loop for users whose SSO email claim doesn't match stored TaskFlow email after Okta→Entra migration

## Problem
After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully in Entra, get redirected back to TaskFlow, and are immediately sent back to Entra. The affected users disproportionately have plus-sign email addresses (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during the Entra migration.

## Root Cause Hypothesis
Entra ID is returning the base email (jane@company.com) in its SAML/OIDC claims, while TaskFlow has the plus-addressed email (jane+taskflow@company.com) stored as the user identifier. When TaskFlow receives a successful authentication but cannot match the returned email to a local account, it fails silently — instead of showing an error, it redirects the user back to the login flow, which triggers another SSO redirect, creating the loop. The same mismatch likely affects users whose emails were consolidated or aliased during migration. The Okta configuration explicitly sent the full plus-addressed email in claims; the Entra configuration does not.

## Reproduction Steps
  1. Have a TaskFlow account registered with a plus-addressed email (e.g., user+taskflow@company.com)
  2. Ensure Entra ID has the user's primary email set to the base form (user@company.com)
  3. Attempt to log into TaskFlow via SSO
  4. Observe: Entra authentication succeeds, redirect back to TaskFlow occurs, then immediate redirect back to Entra, repeating indefinitely

## Environment
TaskFlow with SAML/OIDC SSO, recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox. Not browser- or OS-specific.

## Severity: high

## Impact
~30% of the team is locked out of TaskFlow entirely. Workarounds (incognito windows) are unreliable. Blocks daily work for affected users.

## Recommended Fix
Two-pronged investigation: (1) **Claims configuration:** Check what email claim Entra ID is sending back (inspect SAML assertions or OIDC tokens). Configure Entra to send the full plus-addressed email, or add custom claims mapping to match what Okta sent. (2) **Account matching logic:** Fix TaskFlow's SSO callback handler to handle email mismatches gracefully — when authentication succeeds but no matching account is found, display an error page instead of redirecting to login. Consider implementing case-insensitive, alias-aware email matching (e.g., treating user+tag@domain and user@domain as potential matches). Also audit any users whose emails changed during migration and update TaskFlow records or Entra claims accordingly.

## Proposed Test Case
Create a test user in TaskFlow with email user+tag@company.com. Configure Entra to return user@company.com in the email claim. Attempt SSO login and verify: (a) the user is either matched correctly via alias-aware logic, or (b) a clear error message is displayed — not a redirect loop. Additionally, test with a user whose email matches exactly to confirm no regression.

## Information Gaps
- Exact SAML/OIDC claim values Entra is returning (would confirm the mismatch definitively — developer can check server logs or inspect tokens)
- Why incognito windows sometimes work (may relate to cached Okta session cookies interfering, or timing-dependent token state)
- Whether TaskFlow's SSO callback code has explicit error handling for unmatched accounts or silently falls through to a redirect
