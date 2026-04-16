# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop pattern is a classic symptom of a token or session validation failure after the SSO handoff.

The 30% affected figure is a strong clue. Have you been able to identify any pattern that distinguishes affected users from unaffected ones? Specifically:

- Were the affected users previously logged into TaskFlow with active sessions when the SSO switch happened?
- Do the affected and unaffected users differ in any way in Entra ID — e.g., different groups, different MFA configurations, migrated vs. freshly provisioned accounts, or different email domain formats (like `first.last@company.com` vs `flast@company.com`)?

Knowing what's different about that 30% will help narrow this down significantly.

---

### reporter

Good question. I haven't checked whether they had active sessions during the switch, but I don't think that's it since we did the cutover over the weekend when nobody was logged in.

As for what's different about the affected users — now that you mention it, I think a lot of them are people who use plus addressing in their email, like jane+taskflow@company.com. We had a few people set up that way in Okta. There are also a couple users where we set up email aliases in Entra that are different from what they originally signed up for TaskFlow with. I'm not 100% sure that accounts for all the affected users but it's a pattern I noticed.

---

### triage-agent

That plus-addressing and alias pattern is a very strong clue — here's my working theory:

When TaskFlow receives the token back from Entra ID after authentication, it tries to match the user to an existing account using an email claim. For the affected users, Entra ID is returning a **different email value** than what Okta used to send — for example, Okta may have preserved `jane+taskflow@company.com` as the email claim, but Entra ID is returning the canonical `jane@company.com` (or vice versa). TaskFlow can't find a matching user, so it never creates a session, and the login flow restarts.

Could you check two things to confirm or rule this out?

1. **In TaskFlow's database or admin panel**, what email addresses are stored for a couple of the affected users? (e.g., is it `jane+taskflow@company.com`?)
2. **In your Entra ID app registration**, what claim is mapped as the user's email or identifier being sent to TaskFlow? You can check this under **Enterprise Applications → TaskFlow → Single sign-on → Attributes & Claims**. Specifically, what value is mapped to the `email` or `nameidentifier` claim — is it `user.mail`, `user.userprincipalname`, or something else?

If there's a mismatch between what's stored in TaskFlow and what Entra is sending, that would explain the redirect loop for exactly these users.

---

### reporter

OK I checked a couple of these. For the TaskFlow side, yes — I looked at a few affected accounts in the admin panel and they have the plus-addressed emails stored, like jane+taskflow@company.com. So TaskFlow knows them by that address.

For the Entra ID side, I'm pretty sure the claim is mapped to user.mail, which would be their primary mailbox address — so jane@company.com without the plus part. I didn't set up the Entra app registration myself (our Azure consultant did) but I can see in the claims config that it's sending the primary email.

So yeah, it looks like there is a mismatch there. But one thing I'm not sure about — if the email mismatch means TaskFlow just can't find the user, wouldn't they get an error message instead of a redirect loop? Like a 'user not found' page or something? Instead they just keep going around and around between Microsoft and TaskFlow. I also noticed that some of the affected users can sometimes get in if they use an incognito window, which seems weird if it's just an email matching problem.

---

### triage-agent

You're raising exactly the right questions — the redirect loop (instead of an error page) and the incognito behavior both suggest there's more going on than just the email mismatch alone.

Here's what I suspect is happening: TaskFlow receives the token from Entra, tries to match the email, fails, and instead of showing an error, it falls back to "you're not authenticated, let me redirect you to login" — which sends you right back to Entra, creating the loop. The incognito clue suggests that **stale cookies or cached tokens from the old Okta integration** may also be involved — in a normal browser, TaskFlow might be reading an old session cookie that interferes with the new Entra flow, while incognito starts clean.

To confirm this: **when the affected users try logging in with a normal (non-incognito) browser, have they cleared their cookies for your TaskFlow domain first?** And if they have, does it change the behavior — do they still loop, or do they get a different result (like an error page or successful login)?

This will tell us whether we're dealing with one problem (email mismatch) or two stacked problems (email mismatch + stale session cookies).

---

### reporter

Yeah, I've had a few of the affected users try clearing their cookies. It's kind of inconsistent — sometimes after clearing cookies they can get in if they try right away, but other times they still end up in the loop. It's not a reliable fix. The incognito thing is similarly hit-or-miss, it seems to work more often than just clearing cookies does but not 100% of the time either. I haven't really been able to figure out a pattern for when it works vs when it doesn't. I will say though, even the users who had never logged into TaskFlow before (we hired a couple people recently whose Entra accounts have aliases that differ from what we pre-provisioned in TaskFlow) — they get the loop too, so I don't think it's just leftover Okta cookies.

---

### triage-agent

That detail about new hires hitting the loop too is really helpful — it rules out stale Okta cookies as the root cause and points squarely at the email mismatch.

But the inconsistency you're describing is bugging me. If it were purely an email matching failure, clearing cookies shouldn't help at all — TaskFlow would fail to find the user every single time. The fact that it sometimes works after clearing cookies or in incognito suggests something non-deterministic is going on in the auth flow.

**Can you check TaskFlow's server-side logs during one of these redirect loops?** Specifically, I'm looking for what happens when the authentication callback fires — is there an error logged like "user not found" or "email mismatch," or is it something else (like a session write failure, a race condition, or a token validation error)? If you can grab the relevant log entries from one failed login attempt by an affected user, that would tell us whether we're dealing with one clean problem or two stacked issues.

---

### reporter

Honestly I haven't dug into the server-side logs yet — I'm more of an infrastructure person than a developer so I'd need to figure out where TaskFlow even writes its logs. I can look into that but it might take me a bit.

One thing I did notice though, when I had one of the affected users try logging in while I had the browser dev tools open on their machine — I could see that TaskFlow was sending back a Set-Cookie header in the redirect response coming back from Microsoft. So it looks like it's trying to set a session cookie, but the cookie doesn't seem to actually stick? On the next request back to TaskFlow the cookie isn't there. I'm not sure if that's relevant or if I'm reading the dev tools wrong, but it seemed odd to me.

---

### triage-agent

That cookie observation is actually a huge clue — it may explain the inconsistency you've been seeing.

When modern browsers receive a `Set-Cookie` during a cross-site redirect (like coming back from Microsoft's login page to your TaskFlow domain), they will silently drop the cookie unless it has the right `SameSite` attribute. This is a known issue with OAuth/SSO callback flows.

Since you already had dev tools open and saw the `Set-Cookie` header, could you look at that header one more time and tell me:

1. **What are the cookie attributes?** Specifically, does it include `SameSite=Lax`, `SameSite=Strict`, `SameSite=None`, or no `SameSite` attribute at all? And does it include `Secure`?
2. **Is your TaskFlow instance served over HTTPS?**

This would explain why the loop is intermittent — different browsers handle missing or incorrect `SameSite` attributes differently, and incognito mode can behave differently too. If this is the issue, it means you likely have two stacked problems: the email mismatch for the 30% of users, *and* a cookie configuration issue that makes the redirect loop non-deterministic even when the email does match.

---

### reporter

I pulled it up again on one of the affected user's machines. The Set-Cookie header has SameSite=Strict on it. I also see Secure is there. And yes, we're running TaskFlow over HTTPS. I'm not a web developer so I don't really know what SameSite=Strict means in this context, but if you're saying the browser might be ignoring the cookie because it's coming from a redirect from Microsoft's domain, that would actually make a lot of sense with what I'm seeing. Is that something we can fix on our end or does TaskFlow need to change that?

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop caused by SameSite=Strict session cookie dropped during SSO callback, compounded by email claim mismatch after Okta-to-Entra migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. The loop occurs between TaskFlow and Microsoft's login page. The behavior is intermittent — sometimes clearing cookies or using incognito resolves it temporarily, but not reliably. New users with mismatched emails also experience the issue.","root_cause_hypothesis":"Two stacked issues: (1) **SameSite=Strict cookie** — TaskFlow sets its session cookie with `SameSite=Strict` during the OAuth callback redirect from Microsoft's domain. Modern browsers silently drop cookies with `SameSite=Strict` on cross-site redirect responses, so the session is never established, and TaskFlow redirects back to the IdP, creating the loop. This is the primary loop mechanism and affects all users non-deterministically. (2) **Email claim mismatch** — Entra ID sends `user.mail` (canonical email like `jane@company.com`) while TaskFlow has users stored with plus-addressed emails (`jane+taskflow@company.com`) or old aliases from Okta. Even when the cookie does stick (browser-dependent timing), TaskFlow cannot match the authenticated identity to a local account for these users, so authentication still fails. The combination explains why ~30% of users are disproportionately affected (they have the email mismatch) and why the behavior is intermittent (cookie dropping depends on browser behavior).","reproduction_steps":["1. Have a user whose TaskFlow account email differs from their Entra ID `user.mail` claim (e.g., plus-addressed email like `jane+taskflow@company.com` in TaskFlow, but `jane@company.com` in Entra)","2. Navigate to TaskFlow login page in a standard browser session","3. Click login — user is redirected to Microsoft Entra ID","4. Authenticate successfully at Microsoft","5. Microsoft redirects back to TaskFlow's callback URL","6. Observe in browser dev tools: TaskFlow returns a `Set-Cookie` header with `SameSite=Strict; Secure` — browser silently drops the cookie because it arrived via cross-site redirect","7. TaskFlow finds no session cookie on the next request, treats user as unauthenticated, redirects back to Microsoft","8. Loop repeats indefinitely"],"environment":"TaskFlow served over HTTPS. SSO provider: Microsoft Entra ID (recently migrated from Okta). Entra ID email claim mapped to `user.mail`. Session cookie attributes: `SameSite=Strict; Secure`. Affects approximately 30% of users, particularly those with plus-addressed emails or email aliases that differ between TaskFlow and Entra ID. New hires with pre-provisioned mismatched emails are also affected.","severity":"high","impact":"~30% of the team is completely unable to log into TaskFlow. No reliable workaround exists. New hires with mismatched emails are also locked out. The SSO cookie issue may intermittently affect all users, but those with matching emails may succeed on retry while mismatched-email users face a harder failure.","recommended_fix":"**Fix 1 (Cookie — critical, fixes the loop mechanism):** Change the session cookie's `SameSite` attribute from `Strict` to `Lax` (or `None` if Lax is insufficient for the OAuth callback flow, though `Lax` should work for top-level navigations). This is the standard fix for OAuth/SSO callback flows — `SameSite=Strict` cookies are intentionally blocked on cross-site redirects. Look in TaskFlow's session/authentication configuration for where cookie attributes are set. **Fix 2 (Email matching — fixes the 30% user mismatch):** Either (a) update TaskFlow's user lookup to match on a stable, non-email identifier like Entra's `oid` (object ID) claim or `sub` claim instead of email, or (b) remap the Entra ID claim to send a value that matches what TaskFlow stores (e.g., configure a custom claim or use a different attribute), or (c) update the affected users' emails in TaskFlow's database/admin panel to match what Entra sends. Option (a) is the most robust long-term. Option (c) is the fastest short-term fix. Also consider updating the Entra app registration's claims mapping (under Enterprise Applications → TaskFlow → Attributes & Claims) to send a claim that TaskFlow can reliably match on.","proposed_test_case":"1. **Cookie fix verification:** After changing SameSite to Lax, log in with a user whose email matches in both systems — confirm no redirect loop and session cookie persists after the callback redirect. Verify in dev tools that the Set-Cookie header now has `SameSite=Lax`. 2. **Email mismatch fix verification:** After applying the email fix, log in with a user who previously had a plus-addressed email mismatch — confirm they authenticate successfully and are matched to the correct TaskFlow account. 3. **Regression test:** Confirm that users who were previously unaffected can still log in normally after both fixes.","information_gaps":["Exact TaskFlow server-side logs during the callback — would confirm whether the app logs a 'user not found' error or a session write error (reporter couldn't access logs)","Which specific TaskFlow framework/library manages sessions and cookie settings — needed to locate the SameSite configuration","Whether any users with matching emails also experience the loop (would isolate the cookie issue from the email issue)","TaskFlow version and authentication library in use"]}

---
