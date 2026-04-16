# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report! One key question that could help narrow this down quickly:

Have you noticed any pattern in **what distinguishes the affected ~30% of users** from those who can log in fine? For example:

1. **They had active sessions** during the SSO switch (were logged in when it changed)
2. **They belong to a different group or role** in your identity provider
3. **They use a different browser or device** than the unaffected users
4. **They had previously logged into TaskFlow with Okta** (vs. newer accounts)
5. Something else?

Even a hunch here would be very helpful.

---

### reporter

Good question. I've been trying to figure out a pattern myself. One thing I noticed is that a lot of the affected users have those plus-sign email addresses — like jane+taskflow@company.com. We set those up a while back so people could filter TaskFlow notifications. But not all affected users have that, so there might be something else going on too.

I also think some of the affected people had their emails changed or aliased when we set up Entra ID — like their primary email in Entra might be slightly different from what they originally signed up with in TaskFlow. But I'm not 100% sure on that one, I'd have to check with our identity team.

It doesn't seem to be a browser thing — I've seen it happen on Chrome, Edge, and Firefox.

---

### triage-agent

Thanks, that's a really strong lead. Here's my working theory — let me know if it tracks:

TaskFlow likely matches the authenticated user by comparing the **email claim** from your SSO provider against the email stored in TaskFlow's user database. If Entra ID is returning a different email than what TaskFlow has on file (e.g., `jane@company.com` from Entra vs. `jane+taskflow@company.com` in TaskFlow, or vice versa), the app can't match the user to an account, and the login fails silently by restarting the auth flow — causing the loop.

Could you pick one or two affected users and compare:
1. The **email stored in TaskFlow** for that user
2. The **primary email (or UPN) in Entra ID** for the same person

Are they different? That would confirm whether this is an email mismatch issue.

---

### reporter

Yeah, that theory makes sense to me. I just spot-checked two of the affected users. One of them, Jane, has jane+taskflow@company.com in TaskFlow but Entra is sending jane@company.com as her email. So yeah, those don't match. The other user I checked, Mike, his email in Entra is mike.jones@company.com but in TaskFlow he was originally set up as mjones@company.com — that was his old alias from before we standardized email formats.

So it does look like an email mismatch thing. Though one thing I'll mention — even when I tried manually updating one user's email in TaskFlow to match Entra, they still got the redirect loop at first. They had to clear their cookies and try again before it worked. Not sure if that's relevant or just a caching thing.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop caused by email mismatch between Entra ID claims and TaskFlow user records after SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop during login. The authentication with Entra succeeds, but TaskFlow cannot match the authenticated user to an existing account because the email claim returned by Entra ID differs from the email stored in TaskFlow's user database.","root_cause_hypothesis":"TaskFlow's SSO callback handler matches users by comparing the email claim from the identity provider against stored user emails. When no match is found, instead of returning an error, the app silently restarts the auth flow, causing an infinite redirect loop. Two categories of mismatch have been confirmed: (1) users with plus-addressed emails in TaskFlow (e.g., jane+taskflow@company.com) where Entra returns the base address (jane@company.com), and (2) users whose email aliases changed during the Entra migration (e.g., mjones@company.com in TaskFlow vs. mike.jones@company.com in Entra). A secondary issue exists where stale session cookies persist after the email is corrected, requiring a cookie clear before the fix takes effect.","reproduction_steps":["Identify a user whose email in TaskFlow differs from their primary email or UPN in Entra ID (e.g., plus-addressed email or old alias)","Have that user attempt to log into TaskFlow via SSO","User authenticates successfully with Entra ID","User is redirected back to TaskFlow, which fails to match the Entra email claim to any account","TaskFlow restarts the auth flow, redirecting back to Entra, creating an infinite loop"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox equally. Not browser- or device-specific.","severity":"high","impact":"Approximately 30% of users are completely locked out of TaskFlow. These are existing users with legitimate accounts whose emails don't match the new identity provider's claims.","recommended_fix":"1. **Immediate (data fix):** Audit all TaskFlow user emails against Entra ID primary emails/UPNs and reconcile mismatches. Advise affected users to clear TaskFlow cookies after the fix. 2. **Short-term (auth handler):** Fix the SSO callback to return a clear error message (e.g., 'No account found for this email — contact your admin') instead of silently restarting the auth flow, which prevents the redirect loop for any future mismatches. 3. **Medium-term (robustness):** Consider matching users on a stable, provider-independent identifier (such as the OIDC `sub` claim or an internal user ID) rather than email alone, or implement case-insensitive email normalization that strips plus-addressing and resolves known aliases.","proposed_test_case":"Create a test user in TaskFlow with email 'testuser+tag@company.com'. Configure the SSO mock/stub to return 'testuser@company.com' as the email claim. Attempt login and verify: (a) the user is either matched correctly or shown a clear error — not a redirect loop, and (b) after email reconciliation, the user can log in successfully.","information_gaps":["Exact field Entra ID returns as the email claim (email, preferred_username, or UPN) — may affect normalization strategy","Whether TaskFlow's user lookup is case-sensitive, which could cause additional mismatches beyond the confirmed ones","Whether the cookie/session persistence issue requires a code fix (e.g., clearing session on auth failure) or is just a one-time browser cache concern"]}

---
