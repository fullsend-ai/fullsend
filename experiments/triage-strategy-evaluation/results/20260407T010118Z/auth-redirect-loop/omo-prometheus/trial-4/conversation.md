# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that's a frustrating one, especially mid-migration.

To start narrowing this down: have you identified any pattern that distinguishes the affected ~30% from users who can log in fine? Specifically:

- Were the affected users previously logged in via Okta when the switch happened (i.e., could they have stale session cookies)?
- Do the affected users' UPNs or email addresses in Entra ID differ in format from the working users (e.g., `first.last@company.com` vs `flast@company.com`, or a different domain)?
- Does clearing cookies and browser storage for your TaskFlow domain resolve the loop for any of the affected users?

Any one of these would help me zero in on the right area.

---

### reporter

Good questions. So clearing cookies does seem to help for some users temporarily — a few people reported that clearing everything and trying again got them in, but then the next day the loop came back. Incognito/private windows also seem to work sometimes which is weird.

As for a pattern with the affected users, I've been trying to figure that out. I did notice that several of the affected users have those plus-sign email addresses — like jane+taskflow@company.com — which we set up back when we were on Okta. I'm not sure if that's the whole picture though because I think there are a couple affected users without the plus sign too. Those might be people whose emails changed at some point, like we had some folks whose accounts were set up under an old email alias.

I don't think it's a stale Okta session issue — we made sure everyone logged out before the cutover and we revoked the Okta app. But I could be wrong on that.

---

### triage-agent

That plus-sign email pattern is a strong lead — thanks for digging into that.

Next thing I need: **what do TaskFlow's server-side logs show during one of these redirect loops?** Specifically:

1. Does the OIDC/SAML callback from Entra ID complete successfully (i.e., does TaskFlow receive and validate the token)?
2. After the callback, is there an error or warning before the redirect back to Microsoft — something like a user-not-found, email mismatch, or session creation failure?
3. What claim is TaskFlow using to match the incoming identity to a local user account — is it the `email` claim, the `preferred_username` / UPN, or the Entra `oid`?

Even a short snippet of the relevant log lines from one loop iteration would be very helpful. If you don't have access to the logs, let me know what auth library or framework TaskFlow uses for SSO and I can point you to where to look.

---

### reporter

Honestly I haven't dug into the server-side logs yet — that's a good call though, I should look. I'm the IT admin here but I'm not super deep on TaskFlow's internals. I do have access to the server so I can pull logs, just need to figure out where they live.

What I can tell you is that I poked around in the browser dev tools for one of the affected users and I did see a Set-Cookie header coming back on the redirect response from Entra, so it seems like TaskFlow is trying to set a session. But the cookie doesn't seem to stick — on the next request it's not there. I didn't fully understand why at the time, I figured maybe it was a browser thing.

I'm not sure what claim TaskFlow is matching on. We're on TaskFlow v2.3.1 self-hosted if that helps you figure out the default. I'd guess it's using email but I haven't confirmed that. Let me try to get the logs and I'll post what I find, but if you know where TaskFlow keeps its auth logs that would save me some time.

---

### triage-agent

That cookie observation is actually a really important clue — it may be the core issue here, separate from the email pattern.

Could you go back into dev tools for an affected user and check the **attributes on that `Set-Cookie` header** you saw? Specifically I need:

1. **`SameSite` attribute** — is it `Strict`, `Lax`, `None`, or absent?
2. **`Secure` flag** — is it present? And is your TaskFlow instance served over HTTPS or plain HTTP?
3. **`Domain` and `Path`** values on the cookie

You can find this in the Network tab — look at the response headers on the redirect back from Entra ID (the callback URL, usually something like `/auth/callback` or `/sso/callback`).

The reason I'm asking: if the cookie's `SameSite` attribute is set to `Strict`, it gets silently dropped on cross-origin redirects — which is exactly what an SSO login flow does. This would cause the exact loop you're describing, and it would affect users inconsistently depending on their browser version and cookie state. This might have worked with Okta if the redirect timing or domain relationship was different.

---

### reporter

OK I just pulled it up again in Chrome dev tools for one of our affected users. Looking at the Set-Cookie header on the callback response... it says SameSite=Strict. The Secure flag is there, and yes we're running over HTTPS. The domain looks like it's set to our TaskFlow domain and path is just /.

So you're saying that because the redirect is coming from Microsoft's domain back to our domain, the browser is throwing away the cookie because of the Strict setting? That would actually explain why incognito sometimes works differently. I think when we were on Okta it must have just worked, maybe something about how Okta handled the redirect was different? Not sure on that part.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop caused by SameSite=Strict session cookie being dropped on cross-origin callback","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully at Entra ID, gets redirected back to TaskFlow's callback URL, TaskFlow sets a session cookie, but the browser discards it because SameSite=Strict is incompatible with cross-origin redirects. The next request has no session, so TaskFlow redirects back to Entra ID, creating the loop.","root_cause_hypothesis":"TaskFlow's session cookie is set with SameSite=Strict. Per the SameSite cookie spec, Strict cookies are not sent (and are silently dropped when set) on responses to cross-site navigations — which is exactly what an OIDC/SAML callback redirect from login.microsoftonline.com to the TaskFlow domain is. This likely worked with Okta due to differences in redirect flow timing, intermediate redirects, or the previous cookie configuration. The 30% affected users are likely those whose pre-migration sessions have expired, forcing a fresh SSO flow; the remaining 70% are still using valid session cookies from before the cutover and will hit this issue as those sessions expire.","reproduction_steps":["Ensure no existing TaskFlow session cookie is present (clear cookies or use incognito)","Navigate to TaskFlow login page on v2.3.1 self-hosted instance configured with Microsoft Entra ID SSO","Click login — user is redirected to Entra ID","Authenticate successfully at Entra ID","Observe redirect back to TaskFlow callback URL (e.g., /auth/callback or /sso/callback)","In browser dev tools Network tab, observe Set-Cookie header on callback response has SameSite=Strict","Observe the cookie is not present on the subsequent request, causing TaskFlow to redirect back to Entra ID","Loop repeats indefinitely"],"environment":"TaskFlow v2.3.1, self-hosted, HTTPS, Microsoft Entra ID SSO (migrated from Okta), Chrome browser confirmed affected","severity":"high","impact":"Currently ~30% of users cannot log in. This will likely grow to 100% as existing session cookies expire. Workarounds (clearing cookies, incognito) are temporary and unreliable. Users are fully blocked from accessing TaskFlow when affected.","recommended_fix":"Change the session cookie's SameSite attribute from Strict to Lax. SameSite=Lax permits cookies on top-level cross-origin navigations (like SSO redirects) while still protecting against CSRF on sub-requests. This is the correct setting for applications using SSO. Check TaskFlow's auth/session configuration (likely in the web framework or auth middleware config) for the SameSite setting. If TaskFlow does not expose this as a config option in v2.3.1, check for a newer version that does, or patch the session middleware directly. Also investigate whether the plus-sign email addresses (e.g., jane+taskflow@company.com) cause a secondary user-matching issue — once the cookie fix is in place, test login with a plus-sign email user to confirm no email-claim mismatch exists.","proposed_test_case":"1. Clear all TaskFlow cookies. 2. Initiate SSO login via Entra ID. 3. Verify the callback response sets a session cookie with SameSite=Lax. 4. Verify the cookie persists on the subsequent request and the user lands on the authenticated dashboard without a redirect loop. 5. Repeat with a user whose email contains a plus sign to rule out email-matching issues.","information_gaps":["Server-side auth logs were not examined — there may be additional errors logged during the callback that reveal a secondary issue (e.g., email claim mismatch for plus-sign addresses)","The exact OIDC claim TaskFlow v2.3.1 uses for user matching is unconfirmed — if it uses the email claim, plus-sign addresses may cause lookup failures that are masked by the cookie issue","Whether the SameSite=Strict setting is a TaskFlow default or was explicitly configured is unknown — this affects whether a config change or code patch is needed","Why the Okta flow did not trigger the same issue is not fully understood — may be a difference in redirect chain or cookie config that was overwritten during migration"]}

---
