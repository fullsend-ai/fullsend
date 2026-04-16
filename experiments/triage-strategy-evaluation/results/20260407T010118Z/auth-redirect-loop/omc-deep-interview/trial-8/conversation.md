# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop sounds frustrating. One thing that jumps out is that only ~30% of users are affected. Have you been able to identify any pattern among the affected users? For example: are they in a specific group or role in Entra ID, were their accounts migrated differently, do they use a particular browser, or do they have existing TaskFlow sessions/cookies from before the SSO switch that the unaffected users might not have (e.g., the unaffected users cleared their cache or hadn't logged in recently)?

---

### reporter

Good question. I've been trying to figure out a pattern myself. It doesn't seem to be browser-specific — we've seen it on Chrome, Edge, and Firefox. One thing I noticed is that a lot of the affected users have email addresses with a '+' in them, like jane+taskflow@company.com. We use plus addressing for some people. But I'm not 100% sure that's the only factor because I think there are a couple affected users who don't have that. Some of our users also had their emails changed or aliased when we set up Entra — like their primary email in Entra might be different from what they originally signed up with in TaskFlow. I did notice that clearing cookies and trying again sometimes works temporarily, and one person said incognito mode worked for them, which is weird.

---

### triage-agent

That's a really helpful observation about the plus-addressed emails and the aliased accounts — those are strong leads. To narrow this down further: can you check TaskFlow's server-side logs for one of the affected users during a redirect loop attempt? Specifically, I'm curious whether TaskFlow is receiving the callback from Entra ID and then failing to match the user (e.g., the email in the OIDC token doesn't match what's stored in TaskFlow's user table), or whether the callback never seems to arrive at all. Also, how does TaskFlow identify returning users after SSO — does it match on email address, a unique OIDC subject ID, or something else?

---

### reporter

I don't have easy access to TaskFlow's server-side logs — we're self-hosted but I'm more of an IT admin than a dev, so I'd need to figure out where those live. I did poke around in the browser dev tools for one user though, and I could see that the redirect back from Microsoft does seem to hit TaskFlow — like there's a response from our TaskFlow URL — but then it just bounces right back to Microsoft again. I noticed there was a Set-Cookie header in one of those redirect responses, so it seems like TaskFlow is trying to set a session, but something's not sticking.

As for how TaskFlow identifies users, I'm pretty sure it matches on email address. That's what we configured in the SSO setup at least — the email claim. Which would make sense with what I said about the plus-addressed users and the aliased emails being the ones having trouble.

---

### triage-agent

That cookie detail from the dev tools is really interesting and might be the key. Could you try one more thing with an affected user? In the browser dev tools Network tab during the redirect loop, look at the cookies being *sent* on the request back to TaskFlow — are there any leftover cookies from the old Okta setup still being sent alongside the new ones? Specifically, look for any session or auth cookies that might have been set before the Entra switch. Also, for the user who got in via incognito — did the problem come back for them on their next regular (non-incognito) login attempt, or did it stay fixed after that one successful incognito session?

---

### reporter

I can try to check that next time I'm with one of the affected users. Off the top of my head, I don't remember seeing old Okta cookies specifically, but I wasn't really looking for that. What I do remember is that the Set-Cookie header on the redirect response had SameSite=Strict on it — I only noticed because I was googling cookie issues and that stood out. Not sure if that matters?

As for the incognito user — yeah, the problem came back when they tried logging in normally again the next day. So it wasn't a permanent fix, just a one-time thing.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop after Okta→Entra ID SSO migration for users with plus-addressed or aliased emails","problem":"After switching SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, but are immediately sent back to Microsoft again. The loop persists across browsers. Clearing cookies or using incognito mode temporarily resolves it, but the problem returns on subsequent normal login attempts.","root_cause_hypothesis":"Two compounding issues: (1) PRIMARY — TaskFlow matches users by email address, but the email claim in the Entra ID OIDC token does not match what's stored in TaskFlow's user table for affected users. Users with plus-addressed emails (jane+taskflow@company.com) and users whose emails were aliased during Entra setup are most affected, likely because Entra normalizes or strips the plus-addressing, or returns the new primary alias instead of the original signup email. TaskFlow fails to find a matching user, so it cannot establish a session and redirects back to login. (2) CONTRIBUTING — The session cookie is set with SameSite=Strict, which browsers will not send on the top-level navigation following a cross-site redirect from login.microsoftonline.com. This means even when TaskFlow attempts to set a session cookie during the OIDC callback, the cookie is not included on the subsequent request, causing TaskFlow to see an unauthenticated user and restart the login flow. This may explain why the loop occurs rather than a clean error, and why incognito mode (no stale cookies, fresh context) sometimes works.","reproduction_steps":["Set up TaskFlow with Microsoft Entra ID SSO, configured to match users by email claim","Create a user in TaskFlow with a plus-addressed email (e.g., jane+taskflow@company.com) or with an email that differs from their Entra ID primary email","Attempt to log into TaskFlow via SSO","Observe: authentication succeeds at Microsoft, redirect returns to TaskFlow, but user is immediately redirected back to Microsoft in a loop","Open browser dev tools Network tab to confirm: TaskFlow receives the callback, returns a Set-Cookie with SameSite=Strict, then issues a redirect back to the login flow"],"environment":"Self-hosted TaskFlow instance. Recently migrated from Okta to Microsoft Entra ID. SSO configured to use email claim for user matching. Some users use plus-addressed emails; some had emails changed or aliased during Entra setup. Issue observed on Chrome, Edge, and Firefox.","severity":"high","impact":"Approximately 30% of the team cannot log into TaskFlow at all. No reliable workaround — incognito mode works for a single session but the problem recurs. This blocks daily work for affected users.","recommended_fix":"1. **Immediate — fix SameSite policy:** Change the session cookie's SameSite attribute from Strict to Lax. SameSite=Strict cookies are not sent on top-level navigations originating from a cross-site redirect (which is exactly what OIDC callback flows do). SameSite=Lax is the correct setting for authentication cookies in OIDC/OAuth flows. 2. **Fix email matching:** Investigate how TaskFlow resolves the OIDC email claim against its user table. Add case-insensitive matching, handle plus-addressing normalization, and consider falling back to the OIDC `sub` (subject) claim as the primary identifier rather than email, since email is mutable and can vary across identity providers. 3. **Migration step:** For aliased users, either update TaskFlow's user table to match the Entra ID primary email, or configure the Entra ID app registration to emit a custom claim containing the original email. 4. **Clean up legacy cookies:** Consider invalidating or clearing any old Okta session cookies that may still be present in users' browsers (via a logout-all or cookie expiration).","proposed_test_case":"Create an integration test for the SSO callback handler that: (a) sends an OIDC callback with an email containing a '+' character and verifies the user is matched and a session is created; (b) sends a callback where the token email differs from the stored email by alias and verifies graceful handling (match, error message, or account linking — not a silent redirect loop); (c) verifies the session cookie is set with SameSite=Lax, not Strict; (d) simulates the post-callback redirect and verifies the session cookie is included in the follow-up request.","information_gaps":["Server-side logs showing the exact failure during the callback — whether it's a user-not-found error, a session creation failure, or something else (reporter lacks dev access to retrieve these)","Whether any legacy Okta cookies are still present in affected users' browsers and potentially conflicting","Whether there are affected users who do NOT have plus-addressed or aliased emails (reporter was uncertain about a couple of cases)","The exact OIDC email claim value Entra ID returns for plus-addressed users — whether it strips, encodes, or preserves the '+'"]}

---
