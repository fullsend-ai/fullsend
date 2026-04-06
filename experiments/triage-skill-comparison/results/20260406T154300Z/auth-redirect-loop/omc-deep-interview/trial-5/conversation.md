# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up. To help narrow this down — have you noticed any pattern that distinguishes the affected users from those who can log in fine? For example: are the affected users in different Entra ID groups or assigned different roles? Were their accounts migrated differently from Okta? Do they have existing TaskFlow sessions or cookies from before the switch that the working users might not have (e.g., the working users might have cleared their browser data or used a different browser)?

---

### reporter

Good question — I did notice a pattern actually. The affected users tend to be ones who use plus-addressed emails, like jane+taskflow@company.com. We set those up in Okta originally so people could filter their TaskFlow notifications easily. The other group that seems affected are users whose email addresses changed at some point — like people who got married and changed their last name, so their Okta account was under one email but we aliased the new email in Entra ID. The users who work fine are the ones with straightforward emails that were the same in both systems.

As for cookies — interesting you mention that. I did notice that if affected users clear all their cookies and try again really quickly, sometimes they can get in. And a couple people told me it works in incognito mode sometimes. But it's not consistent.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop caused by email mismatch between Entra ID claims and stored user identities (plus-addressing and renamed accounts)","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Entra ID succeeds, but TaskFlow fails to establish a session and redirects the user back to the IdP. Affected users are those whose email in Entra ID doesn't exactly match what TaskFlow has stored from the Okta era — specifically plus-addressed emails (e.g., jane+taskflow@company.com) and users whose email changed (e.g., name change).","root_cause_hypothesis":"TaskFlow's SSO callback handler matches the incoming identity token's email claim against its user database using exact string comparison. When the email in the Entra ID token differs from the email stored in TaskFlow (due to plus-address normalization differences or email changes during migration), the lookup fails. Without a matching user record, TaskFlow cannot create an authenticated session, so it falls back to the login flow, creating the loop. Stale session cookies from the Okta era may compound the issue by presenting conflicting identity state, which explains the intermittent success after clearing cookies or using incognito mode.","reproduction_steps":["1. Identify a user account that uses a plus-addressed email (e.g., user+taskflow@company.com) or one whose email was changed between Okta and Entra ID","2. Attempt to log into TaskFlow using SSO","3. Authenticate successfully with Microsoft Entra ID","4. Observe the redirect back to TaskFlow, which immediately redirects back to Entra ID in a loop","5. Optionally: compare the email claim in the Entra ID token (decode the JWT or check IdP logs) against the email stored in TaskFlow's user table for that account"],"environment":"TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects users with plus-addressed emails and users whose email changed during migration. Other Entra ID apps work fine for the same users, confirming the issue is in TaskFlow's handling, not in Entra ID configuration.","severity":"high","impact":"~30% of the team cannot reliably log into TaskFlow. No consistent workaround exists (incognito/cookie clearing is intermittent). Blocks daily task management for affected users.","recommended_fix":"1. Inspect TaskFlow's SSO callback handler — find where it matches the incoming email claim against the user database. 2. Normalize email comparison: strip plus-address suffixes and do case-insensitive matching, or match on a stable identifier (e.g., Entra ID object ID / sub claim) rather than email. 3. Add a migration step or admin tool to reconcile user records: map old Okta emails to current Entra ID emails for users with changed addresses. 4. Ensure the session creation failure path returns an informative error rather than silently redirecting back to the IdP. 5. Consider clearing or invalidating pre-migration session cookies on first post-migration login attempt.","proposed_test_case":"Create test users with plus-addressed emails and with email mismatches (stored email differs from IdP email claim). Verify that SSO login succeeds for these users and a valid session is established. Also verify that stale session cookies from a previous IdP configuration do not cause redirect loops.","information_gaps":["Exact email claim field Entra ID is sending (email vs preferred_username vs UPN) — may affect normalization logic","Whether TaskFlow uses SAML or OIDC for SSO — affects where to look for the matching logic","Database schema for user identity storage — whether there's a separate SSO identifier column or only email"],"clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.75,"impact":0.8,"overall_ambiguity":0.175}}

---

