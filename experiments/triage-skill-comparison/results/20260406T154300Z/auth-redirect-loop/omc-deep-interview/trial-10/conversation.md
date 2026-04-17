# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. One thing that would really help narrow this down: have you noticed any pattern among the affected users versus the ones who can log in fine? For example — are the affected users in a specific Azure AD group, do they have a different role or account type in TaskFlow, or were their accounts provisioned differently (e.g., SCIM vs. manual)? Also, does clearing cookies and browser data for TaskFlow resolve the loop for any of them, even temporarily?

---

### reporter

Good question — I did notice a pattern actually. The users who are affected tend to be ones who use plus-addressing in their email (like jane+taskflow@company.com) or users who had their email changed/aliased at some point during the Okta-to-Entra migration. The users who log in fine are the ones whose straightforward primary email is the same as what they always used in TaskFlow.

As for clearing cookies — yes, sometimes that helps temporarily. A few users have reported that if they clear all their cookies and try again right away, they can occasionally get in. Using an incognito/private window also seems to work sometimes, weirdly enough. But it's not consistent, and it's not a real fix for 15 people every morning.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with plus-addressed or migrated emails after Okta-to-Entra ID switch","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users (~15 people) experience an infinite redirect loop when logging in. They authenticate successfully with Entra ID but are immediately redirected back to the login flow. The affected users share a common trait: they either use plus-addressing in their email (e.g., jane+taskflow@company.com) or had their email changed/aliased during the Okta-to-Entra migration. Clearing cookies or using incognito windows occasionally provides temporary relief but is inconsistent.","root_cause_hypothesis":"TaskFlow's session establishment logic likely compares the email claim in the OIDC token returned by Entra ID against the user's stored email in TaskFlow's database. When these don't match — because Entra ID returns the canonical email while TaskFlow has the plus-addressed or old aliased variant (or vice versa) — the app fails to find/create a valid session and restarts the auth flow. The intermittent cookie-clearing fix suggests that stale session cookies from the Okta era may also interfere, causing the app to attempt to validate an old session token against the new IdP, fail, and redirect. The two factors (email mismatch preventing new session creation + stale cookies preventing clean auth attempts) compound to produce the loop.","reproduction_steps":["Set up a TaskFlow user account whose stored email uses plus-addressing (e.g., testuser+taskflow@company.com)","Configure Entra ID so that the user's primary/canonical email is testuser@company.com (without the plus-address)","Attempt to log into TaskFlow via SSO","Observe that after successful Entra ID authentication, the user is redirected back to the login page in a loop","Alternatively: take a user whose email was changed/aliased during the Okta-to-Entra migration and attempt login"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects multiple browsers. Entra ID app registration and redirect URIs verified correct. Affected users can authenticate to other Entra ID apps without issue.","severity":"high","impact":"~30% of the team (approximately 15 users) are blocked from logging into TaskFlow each morning. No reliable workaround exists. Daily productivity loss across the affected user group.","recommended_fix":"1. Inspect the SSO callback handler where TaskFlow matches the incoming OIDC token's email claim to stored user records. Check whether the comparison is exact-match or normalized. 2. Log the exact email claim value Entra ID returns for affected users and compare it to what TaskFlow has stored — confirm the mismatch hypothesis. 3. Implement case-insensitive, plus-address-aware email normalization when matching identity claims to user records (strip the +tag portion before comparison, or match on a stable claim like `sub` or `oid` instead of email). 4. Add a migration script or one-time reconciliation to update stored emails for users whose addresses changed during the Okta-to-Entra migration. 5. Consider clearing or invalidating all pre-migration session cookies to eliminate stale Okta session interference.","proposed_test_case":"Create integration tests for the SSO callback handler that verify: (a) a user with plus-addressed email in the database is matched when Entra ID returns the canonical email without the plus tag, (b) a user whose stored email differs from the Entra ID email claim by alias/domain is still matched via a stable identifier (oid/sub), (c) a user with a stale Okta session cookie is gracefully redirected through a clean auth flow rather than looping.","information_gaps":["Exact OIDC claims configuration in Entra ID (which claim is used as the email — UPN, preferred_username, or email)","Whether TaskFlow matches users by email claim or by a stable OIDC subject identifier (sub/oid)","Server-side logs from the SSO callback showing the specific failure point in the redirect loop","Whether the app uses a session store (Redis, DB) or cookie-based sessions, which affects the stale session cleanup approach"],"clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.75,"impact":0.85,"overall_ambiguity":0.15}}

---
