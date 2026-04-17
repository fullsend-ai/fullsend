# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the redirect loop behavior and the partial-impact pattern are really helpful context.

To help narrow down what's different about the ~30% of affected users: could you walk through the exact steps one of them follows? Specifically:

- Which browser(s) are the affected users using, and have you checked whether the same user experiences the loop in a different browser?
- Were the affected users' accounts set up differently in Entra ID — for example, were they migrated from Okta vs. freshly provisioned?
- Do the affected users have any existing TaskFlow sessions or cookies from before the SSO switch?

Any pattern you've noticed distinguishing the affected group from those who can log in fine would be very useful.

---

### reporter

So we've seen it on Chrome and Edge mostly, since that's what most people use here. I had one affected user try Firefox and they got the same loop there too. I don't think it's browser-specific.

All accounts were migrated from Okta — we didn't provision anyone fresh in Entra. But now that you mention it, I've been trying to figure out the pattern myself. I think a lot of the affected users are ones who use plus-addressed emails — like jane+taskflow@company.com. We have a few people who do that. And there are also a couple users whose emails changed at some point, like they had an old email that got aliased to a new one in our directory. I'm not 100% sure that accounts for all of the affected users but it does seem like there's overlap there.

Regarding cookies — we did try having a couple people clear their cookies and sometimes that actually lets them get in, but it's not consistent. One person said using an incognito window worked once but then it stopped working the next time. So there might be something cookie-related going on too.

---

### triage-agent

Thanks for the insight about the plus-addressed and aliased emails — that's a really promising lead.

To round out the picture, could you share some environment details?

- What version of TaskFlow are you running?
- Is this a self-hosted deployment or cloud-hosted, and what's the server environment (e.g., OS, reverse proxy like nginx/Apache, Node version)?
- Which authentication library or SAML/OIDC module does your TaskFlow instance use for SSO?

These details will help a developer pinpoint where in the auth flow the email matching or session handling might be breaking down.

---

### reporter

We're on TaskFlow v2.3.1, self-hosted. As for the server environment, I'd have to check with our sysadmin on the exact details — I handle the identity/SSO side of things more than the server infrastructure. I honestly don't know off the top of my head which auth library TaskFlow uses under the hood. Is there a config file or something I could look at to find that out for you?

---

### triage-agent

Great question! You can usually find the auth library and SSO configuration in one of these places:

- **`config/auth.yml`** or **`config/sso.yml`** in your TaskFlow installation directory
- **`.env`** or **`docker-compose.yml`** if you're running in Docker — look for environment variables like `AUTH_STRATEGY`, `OIDC_LIBRARY`, or `SAML_PROVIDER`
- **`package.json`** — search for dependencies like `passport-openidconnect`, `node-oidc-provider`, `passport-saml`, or similar

If you can share what you find in any of those, that would be perfect. And when you do check with your sysadmin, the key details that would help a developer investigate are:

- OS and Node.js version on the server
- Whether there's a reverse proxy in front of TaskFlow (nginx, Apache, etc.)

Knowing the auth library in particular will help pinpoint whether the redirect loop is caused by how email claims are being matched — your observation about plus-addressed and aliased emails is a strong lead.

---

### reporter

Thanks for the pointers, I'll take a look and check with our sysadmin on the server details. It might take me a day or so to get back to you on that.

In the meantime, one thing I noticed while poking around in the browser dev tools (I was trying to troubleshoot the cookie issue) — when an affected user gets redirected back from Microsoft to TaskFlow, I can see a Set-Cookie header in the response, but it seems like the cookie isn't actually sticking. I'm not a developer so I might be reading it wrong, but it looked like the cookie had SameSite=Strict on it. Could that be relevant since the redirect is coming from Microsoft's domain back to ours?

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop after Okta→Entra ID migration for users with plus-addressed or aliased emails","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Microsoft but are immediately redirected back to the SSO provider upon returning to TaskFlow. The affected population correlates with users who have plus-addressed emails (e.g., jane+taskflow@company.com) or emails that were aliased/changed in the directory. Clearing cookies and incognito mode provide inconsistent temporary relief.","root_cause_hypothesis":"Two interacting issues are likely: (1) TaskFlow's session cookie is set with SameSite=Strict, which causes the browser to drop the cookie on the cross-domain redirect back from Microsoft's auth endpoint, preventing session establishment and restarting the login flow. (2) The email claim returned by Entra ID may not match the stored user identity for plus-addressed or aliased email accounts — if TaskFlow looks up users by exact email match and Entra ID normalizes or strips the plus-address tag, the lookup fails, no session is created, and the user is sent back to authenticate again. The combination of these two issues explains why cookie-clearing works inconsistently: it may temporarily clear stale state but doesn't fix the underlying identity mismatch.","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO, migrated from Okta","Create or identify a user account with a plus-addressed email (e.g., user+taskflow@company.com) or an aliased email","Attempt to log in via SSO","Observe: user authenticates with Microsoft successfully, is redirected back to TaskFlow, and is immediately redirected to Microsoft again in a loop"],"environment":"TaskFlow v2.3.1, self-hosted deployment. Browser-agnostic (reproduced on Chrome, Edge, Firefox). SSO provider: Microsoft Entra ID (migrated from Okta). Server OS, Node version, reverse proxy, and auth library details pending from reporter's sysadmin.","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. These users are fully blocked from using the application. The affected group includes users with plus-addressed or aliased email addresses.","recommended_fix":"Investigate two areas: (1) **Cookie SameSite policy** — Check the session cookie configuration and change SameSite from Strict to Lax, which permits cookies to be sent on top-level navigations from external origins (the standard setting for OAuth/OIDC redirect flows). (2) **Email claim matching** — Compare the email claim returned by Entra ID (inspect the id_token or userinfo response) against the stored user identifiers in TaskFlow's database. Check whether the auth module does exact-match lookup and whether Entra ID normalizes plus-addressed emails or returns a different primary alias than Okta did. The fix likely involves normalizing email comparison (stripping plus-address tags or matching against all known aliases) and correcting the SameSite cookie attribute.","proposed_test_case":"Create test users with plus-addressed emails and aliased emails in the identity provider. Verify that: (a) the SSO login flow completes without redirect loops, (b) the session cookie is set with SameSite=Lax and persists across the cross-domain redirect, and (c) the email claim from the identity provider correctly resolves to the user record regardless of plus-addressing or alias changes.","information_gaps":["Exact server environment (OS, Node.js version, reverse proxy) — reporter is checking with sysadmin","Which auth library/module TaskFlow uses for SSO — reporter is looking into config files","Whether the Entra ID token's email claim actually differs from what's stored in TaskFlow's user database (requires developer inspection)","Whether the SameSite=Strict observation from browser dev tools is the root cause or a contributing factor"]}

---
