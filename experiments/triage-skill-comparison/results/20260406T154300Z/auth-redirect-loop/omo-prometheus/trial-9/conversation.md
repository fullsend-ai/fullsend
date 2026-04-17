# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. The 30/70 split is a strong clue — let's use it.

Have you been able to identify any pattern that distinguishes the affected users from the ones who can log in fine? Specifically:

- Do the affected users share any common attribute — e.g., a specific role, group, department, or account creation date in your identity provider?
- Have you checked whether the affected users have any leftover Okta session cookies or cached credentials in their browsers? Does the issue persist if they try in an incognito/private window or a different browser?

Even a rough pattern ("it seems like it's mostly people in engineering" or "it's the ones who had MFA enrolled under Okta") would help narrow this down significantly.

---

### reporter

Good questions. So I've been looking at this and I think I found a pattern — it seems like the affected users are the ones who use plus addressing in their email (like jane+taskflow@company.com), and also a few people whose emails were changed or aliased at some point. The users who log in fine are the ones whose plain email address is the same in both systems.

As for incognito/private windows — funny you mention that, because a couple of affected users told me that incognito mode sometimes works, but not consistently. Clearing all cookies and trying again quickly also sometimes gets them in, but it's not reliable. The issue happens across Chrome, Edge, and Firefox, so it doesn't seem browser-specific.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra ID SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully at Entra ID but are immediately redirected back to the login flow upon returning to TaskFlow. The affected population correlates strongly with users who use plus addressing (e.g., jane+taskflow@company.com) or whose email addresses were changed or aliased at some point.","root_cause_hypothesis":"TaskFlow's authentication callback matches the email claim from the OIDC/SAML token against stored user records to establish a session. Okta and Entra ID handle email claims differently — most likely Okta preserved the full plus-addressed email in the token's email claim (or used a custom claim mapping), while Entra ID returns the canonical/primary email address (stripping the plus tag or returning the primary alias). When TaskFlow receives the token, it cannot find a matching user record, fails to create a session, and redirects back to login, creating the loop. The inconsistent success in incognito likely reflects race conditions or intermittent claim variations in cached vs fresh token exchanges.","reproduction_steps":["Create or identify a user account in TaskFlow whose stored email uses plus addressing (e.g., user+taskflow@company.com)","Configure SSO login via Microsoft Entra ID","Attempt to log in as that user","Observe: authentication at Entra ID succeeds, but the redirect back to TaskFlow triggers another redirect to Entra ID, looping indefinitely","Compare with a user whose stored email matches their Entra ID primary email exactly — login succeeds"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Issue is cross-browser (Chrome, Edge, Firefox) and not resolved by incognito mode or cookie clearing. Affected users can authenticate to other Entra ID apps without issue.","severity":"high","impact":"Approximately 30% of the team is unable to log into TaskFlow reliably. No consistent workaround exists. This is a complete access blocker for affected users.","recommended_fix":"1. Inspect the OIDC/SAML token claims returned by Entra ID for an affected user — compare the email claim value against what TaskFlow has stored in its user database. Confirm the mismatch. 2. Check which claim field TaskFlow uses for user lookup (e.g., `email`, `preferred_username`, `upn`, `sub`) and whether it changed between Okta and Entra ID token formats. 3. Fix the user-matching logic: either (a) normalize emails by stripping plus tags before comparison, (b) add support for matching on a stable identifier like `sub`/`oid` rather than email, or (c) map the correct Entra ID claim that includes the full plus-addressed email via custom claim mappings in the Entra ID app registration. 4. For aliased users, ensure the lookup checks against all known aliases or uses the immutable object ID. 5. As an immediate workaround, affected users can update their stored email in TaskFlow to match their Entra ID primary email (if the app allows self-service profile edits or via admin action).","proposed_test_case":"Create test users with three email patterns: (1) plain email matching Entra ID primary email, (2) plus-addressed email (user+tag@domain), (3) email that differs from the Entra ID primary due to aliasing. Verify that all three can authenticate via SSO and are correctly matched to their TaskFlow accounts. Additionally, test that existing sessions are not disrupted when email normalization logic is applied.","information_gaps":["Exact claim field(s) TaskFlow uses for user matching (email vs. upn vs. sub vs. oid)","Whether Entra ID custom claim mappings were configured to replicate Okta's claim format","Exact token payload comparison between a working and non-working user","Whether the TaskFlow user database stores the plus-addressed email or the canonical form"]}

---
