# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop sounds frustrating for the affected users.

Have you been able to identify any pattern that distinguishes the 30% of users who are stuck in the loop from those who can log in successfully? For example: different roles or group memberships in Entra ID, different browsers, whether they had an existing TaskFlow session/cookies from before the SSO switch, or whether clearing cookies resolves it for them?

---

### reporter

Good question. I've been trying to figure out the pattern myself. One thing I noticed is that a lot of the affected users have email addresses with a '+' in them — like jane+taskflow@company.com. We use plus addressing for some people. But I'm not 100% sure that's the only factor because I think there are a couple users without the '+' who are also affected, and they might be people whose emails changed at some point.

As for clearing cookies — yes, sometimes that helps temporarily. A few people have gotten in after clearing all their cookies, but it seems hit or miss. One person told me it worked in an incognito window but I haven't verified that with everyone. Browser-wise, we mostly use Chrome and Edge, and I've seen it happen on both.

They definitely didn't have this problem before we switched from Okta, so it's something about the Entra ID migration specifically.

---

### triage-agent

That's a really useful observation about the plus-addressed emails — that could very well be the key here.

To help narrow this down: are you able to check TaskFlow's server-side logs during one of these redirect loops? Specifically, I'm looking for what happens when the auth callback fires — does TaskFlow receive the token from Entra ID and then fail to match the user, throw a validation error, or does the callback not seem to fire at all? If there are any error messages or stack traces in the logs around the authentication/callback handler, those would be very helpful.

If logs aren't readily available, another angle: for one of the affected users with a '+' in their email, could you check whether the `email` or `preferred_username` claim in the Entra ID token contains the '+' literally (e.g. `jane+taskflow@company.com`) or URL-encoded (e.g. `jane%2Btaskflow@company.com`)?

---

### reporter

I'm not super familiar with digging into server logs, but I can try to take a look. I do have access to the server since we self-host TaskFlow. Give me a bit and I'll see what I can find.

As for the token claims — I did actually peek at the browser dev tools for one of the affected users (jane+taskflow@company.com) and I noticed something interesting. The redirect back from Microsoft seems to set a cookie, but then on the next request to TaskFlow, the cookie doesn't seem to be there? I'm not a web dev so I might be reading it wrong, but it looked like the Set-Cookie header was in the response but then the cookie just... wasn't sent on the follow-up request. I saw this in Chrome's network tab.

I haven't specifically checked what email comes back in the token from Entra ID versus what Okta used to send. Is there a quick way to check that? I know how to get to the dev tools but I'm not sure where to look for the actual token contents.

---

### triage-agent

That cookie observation is really valuable — it might be the smoking gun here. Could you do one more check in Chrome DevTools for me?

For one of the affected users, go to the Network tab and find the response from TaskFlow that has the `Set-Cookie` header (the one right after the redirect back from Microsoft). Can you copy the full `Set-Cookie` header value and share it here? I'm specifically interested in whether the cookie value contains the `+` character or the email address — some browsers will reject cookies with certain characters.

Also, while you're there: does the `Set-Cookie` header include `SameSite=` and `Secure` attributes? If TaskFlow was configured for Okta's domain and the cookie settings weren't updated for Entra ID's different redirect flow, that could also explain why the cookie isn't sticking.

You can find this by clicking on the request in the Network tab, then looking under the "Response Headers" section.

---

### reporter

OK I just pulled it up again for Jane's account. The Set-Cookie header looks something like this (I'm paraphrasing since I didn't copy it exactly):

`Set-Cookie: taskflow_session=<long string>; Path=/; Secure; HttpOnly; SameSite=Strict`

I don't think the cookie value itself had her email in it — it looked like a random session token. But I did notice it says `SameSite=Strict` like you asked about. I don't know if that's significant? We didn't change any TaskFlow cookie settings during the migration — we just updated the SSO provider configuration to point to Entra ID instead of Okta.

Is the SameSite=Strict thing a problem? I vaguely remember reading something about that causing issues with redirects but I'm not sure if it applies here.

---

### triage-agent

You're spot on — `SameSite=Strict` is very likely the problem here. In an SSO redirect flow, the browser treats the redirect back from Microsoft as a cross-site navigation, and `SameSite=Strict` cookies won't be sent with that request. This can break the authentication handshake.

One more thing that would help confirm this: in the Chrome Network tab for one of Jane's login attempts, can you look at the request that goes **to** TaskFlow's callback URL (the one right after Microsoft redirects back — it should have an authorization code in the URL)? Specifically:

1. Does that request include **any** cookies in its **Request Headers**? (Look for a `Cookie:` header on that request.)
2. What's the HTTP status code of that response — is it a `302` redirect (sending the user back to login), or a `200`, or something else?

This will tell us whether the redirect loop is happening because TaskFlow can't read its own cookies during the callback, which would confirm `SameSite=Strict` as the root cause and point to a straightforward fix.

---

### reporter

OK I just looked at it again. For the request back to TaskFlow after Microsoft redirects (it had something like ?code=... in the URL), I don't see a Cookie header on that request at all — or at least not the taskflow_session one. There might have been some other cookies but not the session one that was just set. And the response was a 302 that sent the user right back to the login page, which then kicked off the whole loop again.

So yeah, it seems like TaskFlow sets the cookie but then can't read it on the very next request because of that SameSite thing. Is there a setting somewhere in TaskFlow to change that to something else? We're on v2.3.1 self-hosted if that helps.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop caused by SameSite=Strict session cookie breaking Entra ID SSO callback","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully with Entra ID, but when redirected back to TaskFlow's callback URL, the session cookie is not sent with the request. TaskFlow cannot find the session, returns a 302 to the login page, and the cycle repeats.","root_cause_hypothesis":"TaskFlow's session cookie is set with SameSite=Strict. When Entra ID redirects the user back to TaskFlow's callback URL (a cross-site navigation from login.microsoftonline.com), the browser withholds SameSite=Strict cookies per the cookie spec. TaskFlow's callback handler finds no session, treats the user as unauthenticated, and redirects back to login. This likely worked with Okta due to differences in redirect flow timing or because sessions were already established, but breaks with the Entra ID migration. The inconsistent 30% affected rate is likely explained by some users having pre-existing valid session cookies from before the migration, or browser/cookie state differences.","reproduction_steps":["Use TaskFlow v2.3.1 self-hosted with Entra ID SSO configured","Clear all TaskFlow cookies (or use a fresh incognito window — though results are inconsistent)","Click Login and complete authentication in Microsoft Entra ID","Observe the redirect back to TaskFlow's callback URL (?code=...)","In Chrome DevTools Network tab, confirm the callback request has no taskflow_session cookie in its Request Headers","Observe a 302 response redirecting back to the login page, restarting the loop"],"environment":"TaskFlow v2.3.1, self-hosted. SSO provider: Microsoft Entra ID (migrated from Okta). Browsers: Chrome and Edge both affected. Session cookie config: Secure; HttpOnly; SameSite=Strict.","severity":"high","impact":"Approximately 30% of users are completely unable to log in. No reliable workaround exists — clearing cookies and incognito mode help inconsistently. All affected users are locked out of the application.","recommended_fix":"Change the session cookie's SameSite attribute from Strict to Lax. SameSite=Lax allows cookies to be sent on top-level navigations (like SSO redirects) while still protecting against CSRF on subresource requests. Locate the session/cookie configuration in TaskFlow (likely in the auth middleware or server config) and update SameSite=Strict to SameSite=Lax. After the fix, verify the full SSO flow works for users with and without plus-addressed emails. Do not use SameSite=None unless necessary, as Lax provides the correct balance of security and SSO compatibility.","proposed_test_case":"Automated integration test: initiate an SSO login flow with a mock identity provider that redirects back to the callback URL as a cross-site navigation. Assert that the session cookie is present on the callback request and that the response is a 200 or redirect to the dashboard (not back to login). Additionally, test with a user whose email contains a '+' character to rule out any secondary encoding issue.","information_gaps":["Whether the plus-addressing correlation is a true secondary issue (e.g., email encoding mismatch in user lookup) or merely coincidental with the SameSite problem — this should be re-evaluated after the SameSite fix is applied","Exact server-side logs/error messages during the failed callback — not needed for the fix but may reveal additional context","Whether TaskFlow's cookie configuration is user-editable via config file or requires a code change"]}

---
