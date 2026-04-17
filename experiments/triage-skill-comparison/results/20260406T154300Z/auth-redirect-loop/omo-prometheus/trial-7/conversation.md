# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the 30% split is a really useful clue.

Have you been able to identify any pattern among the affected users? Specifically:

- Were these users who had **active TaskFlow sessions** (or cached cookies/tokens) from the old Okta setup when the switch happened, versus users who hadn't logged in recently?
- Do affected vs. unaffected users differ in any way in **Entra ID group memberships or assigned roles** in the app registration?
- Is the issue **browser-specific** — does an affected user succeed if they try an incognito/private window or a different browser?

Any one of these would help narrow down whether this is a stale-session issue, a claims-mapping issue, or something else entirely.

---

### reporter

Good questions. Let me think through what I know:

1. I don't think it's about active sessions — we actually had everyone clear their browser data before the switch, and some of the affected users hadn't logged into TaskFlow in weeks. So I don't think stale Okta sessions are the issue.

2. I checked Entra ID and the affected users don't have different group memberships or roles. They're all assigned to the app the same way.

3. This is interesting — yes, a couple of affected users told me that opening an incognito window sometimes lets them get in. It doesn't work every time, but it works more often than their normal browser. And it happens across Chrome, Edge, and Firefox, so it's not browser-specific.

One thing I did notice is that a lot of the affected users have '+' in their email addresses — like jane+taskflow@company.com. We use plus addressing for some of our team. Also a few users who had their email changed or aliased when we set up Entra seem to be affected. Not sure if that's relevant but figured I'd mention it.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully with Entra, are redirected back to TaskFlow, but TaskFlow immediately redirects them back to Entra. Affected users correlate strongly with having '+' characters in their email addresses (plus addressing) or having had their email changed/aliased during the Entra ID setup.","root_cause_hypothesis":"The most likely root cause is that TaskFlow's OAuth callback handler is not correctly parsing the email claim from the Entra ID token for users with special characters in their email. Specifically: (1) The '+' character is encoded as '%2B' or interpreted as a space in URL query parameters during the redirect. When TaskFlow extracts the user's email from the callback or token, it either URL-decodes '+' to a space or otherwise mangles the address, causing the user lookup to fail. (2) For aliased users, the email claim in the Entra token (e.g. the UPN or preferred_username) doesn't match the email stored in TaskFlow's user table from the Okta era. In both cases, TaskFlow fails to establish a session because it can't match the authenticated identity to a local user record, so it treats the user as unauthenticated and restarts the OAuth flow, creating the loop.","reproduction_steps":["Set up a user in Entra ID with a plus-addressed email (e.g. testuser+tag@company.com) and assign them to the TaskFlow app registration","Attempt to log into TaskFlow with that user","Observe: Entra authentication succeeds, redirect back to TaskFlow occurs, but TaskFlow immediately redirects back to Entra, creating an infinite loop","Compare with a user whose email has no '+' or special characters — login succeeds"],"environment":"TaskFlow instance recently migrated from Okta to Microsoft Entra ID SSO. Affects Chrome, Edge, and Firefox equally. Entra ID app registration and redirect URIs have been verified as correct. Affected users can authenticate to other Entra-backed apps without issue.","severity":"high","impact":"~30% of the team cannot log into TaskFlow at all (no reliable workaround — incognito works intermittently). This blocks their daily workflow. The affected population will grow if more users adopt plus addressing or have email changes.","recommended_fix":"1. **Inspect the OAuth callback handler** — check how the email/UPN claim from the Entra ID token is extracted and used for user lookup. Look for URL-decoding issues where '+' might be converted to a space. 2. **Check the user-matching query** — verify whether it compares the token's email claim against the stored user email using exact string match. If so, ensure the comparison accounts for URL encoding and uses the raw claim value from the parsed JWT, not from URL query parameters. 3. **Audit the user identity mapping** — for users whose emails changed during migration, ensure TaskFlow's user records were updated to match the Entra ID email/UPN. Consider matching on a more stable identifier (like the Entra `oid` or `sub` claim) rather than email. 4. **Add logging** — log the exact email claim received from Entra and the lookup result in TaskFlow's auth flow to confirm the mismatch.","proposed_test_case":"Create integration tests for the OAuth callback handler that authenticate users with: (a) a standard email, (b) a plus-addressed email (user+tag@domain.com), (c) an email containing other special URL characters. Verify that in all cases the correct user record is matched and a session is established without redirect. Additionally, test that a user whose email in the identity provider differs from their stored TaskFlow email (simulating an alias change) is handled gracefully — either matched via a secondary identifier or given a clear error rather than a redirect loop.","information_gaps":["Which specific claim TaskFlow uses for user matching (email, UPN, sub, oid) — inspecting the code will confirm","Whether TaskFlow's auth logs show the received email claim value for affected users (would immediately confirm the encoding hypothesis)","Whether the intermittent incognito success is due to the browser not sending a stale cookie that triggers a different code path"],"confidence":{"root_cause":"medium","reproduction":"high","severity_assessment":"high"}}

---
