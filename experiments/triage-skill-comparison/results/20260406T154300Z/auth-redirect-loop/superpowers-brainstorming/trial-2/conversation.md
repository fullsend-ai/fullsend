# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. The fact that only ~30% of users are affected is a really useful clue.

Have you noticed any pattern among the affected users? For example:

1. **Stale sessions** — Were the affected users logged into TaskFlow (via Okta) at the time of the switch, while unaffected users were not?
2. **Group/role membership** — Do the affected users belong to a different Entra ID group or have a different role assignment than the ones who can log in?
3. **Browser state** — Does clearing cookies/using incognito fix the issue for affected users?
4. **Something else** you've noticed

This will help narrow down whether the issue is client-side (cached auth state from Okta) or server-side (claim mapping, token validation).

---

### reporter

Good questions. For #1, I don't think it's related to who was logged in during the switch — we did the migration over the weekend when nobody was working. For #2, I checked and the affected users span multiple groups and roles, so I don't think that's it. For #3, yes actually! Clearing cookies and trying again sometimes works, and a couple of people have had luck using incognito/private browsing windows, though it's not 100% reliable even then.

One thing I did notice — and I'm not sure if this is relevant — is that a lot of the affected users have email addresses with a '+' in them, like jane+taskflow@company.com. We use plus addressing for some of our team. Also a few of the affected users had their emails changed or aliased when we set up Entra ID. The users who log in fine seem to mostly be the ones with straightforward email addresses that were the same in both Okta and Entra.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta → Entra ID migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully with Entra ID but TaskFlow fails to establish a session, sending them back to the IdP repeatedly. The affected population correlates strongly with users whose email addresses contain a '+' character (plus addressing) or whose email addresses changed/were aliased during the Entra ID setup.","root_cause_hypothesis":"TaskFlow's SSO callback handler matches the incoming identity token's email claim against stored user records to look up or provision the local session. Entra ID likely returns the email claim in a different format than Okta did — either URL-encoding the '+' as '%2B', stripping the plus-address tag entirely, normalizing casing differently, or returning a different alias than the one stored in TaskFlow's user table. When the lookup fails, TaskFlow treats the user as unauthenticated and redirects back to the IdP, creating the loop. The partial success of clearing cookies suggests that stale Okta OIDC state cookies (nonce, session, or CSRF tokens) may also interfere with the new auth flow on some attempts.","reproduction_steps":["Create or identify a TaskFlow user account whose email contains a '+' character (e.g. jane+taskflow@company.com)","Attempt to log in via the Microsoft Entra ID SSO flow","Observe that after successful Entra ID authentication, the browser is redirected back to TaskFlow and then immediately redirected to Entra ID again in a loop","Compare the email claim value in the Entra ID token (decode the id_token JWT) against the email stored in TaskFlow's user database for that user"],"environment":"TaskFlow instance recently migrated from Okta to Microsoft Entra ID SSO. Affects users with plus-addressed emails or emails that changed during migration. Browser-independent (occurs across browsers, partially mitigated by clearing cookies or incognito).","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely. Workarounds (clearing cookies, incognito) are unreliable. This blocks daily work for affected users.","recommended_fix":"1. **Inspect the token claims:** Log or decode the id_token returned by Entra ID for an affected user and compare the email/preferred_username/UPN claim against what TaskFlow has stored. Identify the exact mismatch (encoding, alias, normalization). 2. **Fix user lookup:** Normalize email comparison in the SSO callback — at minimum, URL-decode the claim value and do case-insensitive matching. Consider matching on a stable claim like `oid` (Entra object ID) or `sub` rather than email. 3. **Backfill user records:** For users whose emails changed during migration, either update TaskFlow's stored emails to match what Entra ID returns, or add a mapping/alias table. 4. **Clear stale auth cookies:** On the SSO callback endpoint, clear any legacy Okta session cookies (e.g. old nonce or CSRF cookies) that may interfere with the new flow.","proposed_test_case":"Write an integration test that simulates an SSO callback with an id_token containing a plus-addressed email (e.g. 'jane+taskflow@company.com') and verifies that TaskFlow correctly matches it to the existing user record and establishes a session without redirecting back to the IdP. Add a second case where the token email is a known alias that differs from the stored email to verify alias resolution.","information_gaps":["Exact claim name Entra ID is returning the email in (email vs preferred_username vs UPN) — needs a token decode to confirm","Whether TaskFlow uses a library for OIDC or has custom callback handling — affects where the fix goes","Whether any legacy Okta OIDC cookie names conflict with the new Entra ID flow"]}

---

