# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the fact that only ~30% of users are affected is a really useful clue. Have you noticed any pattern that distinguishes the affected users from the ones who can log in successfully? For example: did the affected users have active TaskFlow sessions at the time of the SSO switch, are they in a different Entra ID group or role assignment, or are they using a particular browser? Even a hunch here would help — sometimes the pattern isn't obvious until you start looking.

---

### reporter

Good question — I've been trying to figure out the pattern myself. It doesn't seem to be browser-related since affected users hit the issue on Chrome, Edge, and Firefox. And it's not about active sessions either, since we had everyone log out before the switch.

What I *have* noticed is that a lot of the affected users have email addresses with a '+' in them — we use plus addressing for some folks, like jane+taskflow@company.com. Also, a few users who don't have the '+' thing are affected too, and those tend to be people whose emails changed at some point — like they originally signed up with one email and then we aliased them to a new one in Entra ID.

The users who can log in fine all seem to have straightforward email addresses that haven't changed. That's about as much of a pattern as I've been able to spot so far.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop caused by email claim mismatch after Okta-to-Entra-ID SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Entra ID succeeds, but TaskFlow fails to establish a session, sending the user back to the IdP. The affected users predominantly have plus-addressed emails (e.g., jane+taskflow@company.com) or have had email address changes/aliases applied in the directory.","root_cause_hypothesis":"TaskFlow matches the incoming SSO identity to its local user records using an email claim from the IdP token. Entra ID formats or normalizes this email claim differently than Okta did — particularly for plus-addressed emails and aliased accounts. When the email in the Entra ID token doesn't exactly match what TaskFlow has stored (from the Okta era), the user lookup fails silently. Without a matched user, TaskFlow cannot create a session, so the auth middleware treats the user as unauthenticated and redirects them back to the IdP, creating the loop. The few affected users without '+' addresses are likely those whose email changed — their Entra ID token carries the new/primary email while TaskFlow still has the old one.","reproduction_steps":["Create or identify a TaskFlow user account whose stored email contains a '+' character (e.g., jane+taskflow@company.com)","Configure TaskFlow SSO to use Microsoft Entra ID","Attempt to log in as that user via SSO","Observe that after successful Entra ID authentication, the browser enters a redirect loop between TaskFlow and the IdP","Compare the email claim in the Entra ID token (decode the JWT or check IdP logs) with the email stored in TaskFlow's user table for that user"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers (Chrome, Edge, Firefox). Server-side issue, not client-specific.","severity":"high","impact":"~30% of the team is completely locked out of TaskFlow with no workaround. This blocks their work and will affect more users over time as email changes accumulate.","recommended_fix":"1. Inspect the SSO callback handler to identify which claim TaskFlow uses for user matching (likely `email` or `preferred_username`). 2. Compare the claim value from Entra ID tokens against stored user records for affected users — confirm the mismatch. 3. Fix the matching logic: either normalize emails before comparison (e.g., strip plus-addressing suffixes if Entra ID does so), or switch to a stable, immutable identifier like the `sub` (subject) claim or `oid` (object ID) for user matching instead of email. 4. For the aliased-email users, either update TaskFlow's stored emails to match Entra ID, or implement a migration script that maps old emails to new ones. 5. As a short-term fix, consider adding a fallback lookup that tries matching on a secondary identifier when the primary email lookup fails.","proposed_test_case":"Unit test for the user-matching function: given a stored user with email 'jane+taskflow@company.com' and an incoming SSO token with email claim 'jane+taskflow@company.com' (exact match), 'jane@company.com' (stripped plus address), or 'JANE+TASKFLOW@COMPANY.COM' (case variation), verify that all variants resolve to the correct user and a session is created. Additionally, test that a user whose stored email is 'old@company.com' can be matched when the token carries 'new@company.com' if a mapping exists.","information_gaps":["Exact claim(s) TaskFlow uses for user identity matching (email, preferred_username, sub, oid, etc.)","Whether Entra ID is actively stripping or transforming the '+' in plus-addressed emails, or if the mismatch is due to a different claim being sent entirely","TaskFlow's SSO callback handler code — whether it logs match failures or silently redirects","Whether the non-plus-addressed affected users had their Okta accounts linked by a different identifier than email"]}

---
