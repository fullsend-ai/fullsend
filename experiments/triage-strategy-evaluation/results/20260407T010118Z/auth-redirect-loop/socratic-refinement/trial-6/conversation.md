# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — this is really helpful context.

The 30% vs 70% split is interesting and probably the fastest path to figuring this out. Have you noticed anything that distinguishes the affected users from the ones who can log in fine? For example, are they in different groups or roles in Entra ID, using different browsers, on different networks, or did some of them previously have accounts created differently in TaskFlow (e.g., invited vs. self-registered, different email domains)?

Even a hunch about what they have in common would be useful — sometimes the pattern isn't obvious until you start listing who's affected.

---

### reporter

Good question. I've been trying to figure out the pattern myself. One thing I noticed is that a few of the affected users have those plus-sign email addresses — like jane+taskflow@company.com — we use that for some people to filter their TaskFlow notifications. But I'm not sure that's all of them.

Also, I think some of the affected users had their email addresses changed or aliased at some point when we were setting up Entra. Like, a couple people had old accounts under a different email and we added their current email as an alias in Entra. I'm not 100% sure that's related though.

Browser-wise, it seems to happen on Chrome and Edge both, so I don't think it's a browser thing. Oh, and one weird thing — a couple users said they got in once by using an incognito window, but it doesn't always work.

---

### triage-agent

That's really useful — both the plus-sign emails and the aliased accounts feel like they could be part of the same puzzle.

Here's where my thinking is going: when TaskFlow receives the authentication response from Entra, it needs to match that person to an existing user in its own database. If there's a mismatch between what Entra sends back (e.g., the user's primary email or UPN) and what TaskFlow has stored, that could explain the loop — TaskFlow authenticates successfully but can't find the local user, so it kicks them back to login.

Could you check what identifier TaskFlow is receiving from Entra for one of the affected users? For example, if you can look at TaskFlow's authentication logs (or even the browser's network tab during the redirect loop), what does the `email` or `preferred_username` claim in the token look like — and does it match what's stored in TaskFlow's user record for that person? I'm especially curious whether the plus-sign users are getting their full `jane+taskflow@company.com` from Entra or just `jane@company.com`, and whether the aliased users are getting their old email or their current one.

---

### reporter

I had one of the affected users (a plus-sign one) try logging in while I watched the network tab in Chrome. I'm not super familiar with reading JWT tokens but I did notice that the redirect back from Microsoft seems to have the email as just jane@company.com rather than jane+taskflow@company.com. So yeah, it looks like Entra is stripping the plus part. That would explain why TaskFlow can't find them, I think?

For the aliased users, I haven't checked yet but I'd guess it's a similar thing — Entra is probably sending whatever their primary email is in Entra, which might not match what they originally signed up with in TaskFlow.

One other thing I noticed in the network tab — there's a Set-Cookie header on the redirect response coming back from the login, but I'm not sure if the cookie is actually sticking. I saw the redirect happen like 3-4 times in a row really fast before I stopped it. Not sure if that's relevant or just a symptom of the loop.

---

### triage-agent

Thanks for digging into the network tab — that really clarifies things. Here's where I think we are:

The redirect loop for the plus-sign users is almost certainly because Entra sends back `jane@company.com` while TaskFlow has `jane+taskflow@company.com` on file. TaskFlow authenticates them successfully at the Entra side, but then can't match the incoming email to a local account, so it sends them back to login. I'd bet the aliased users are hitting the same kind of mismatch — Entra sends their current primary email, but TaskFlow has whatever they originally signed up with.

The cookie detail you mentioned is the thing I'd like to understand better, though, because it might be a separate layer of the problem. When you saw that `Set-Cookie` header during the rapid redirects — do you remember if the cookie's domain and path looked correct for your TaskFlow instance? And did the `Set-Cookie` header have any `SameSite` or `Secure` attributes on it? I'm wondering whether the session cookie is getting rejected by the browser during the cross-origin redirect back from Entra (especially if TaskFlow's auth cookie policy wasn't updated as part of the SSO migration), which could cause loops even for users whose emails match correctly — and might explain why incognito works intermittently (clean cookie jar, no conflicts with old Okta session cookies).

---

### reporter

I went back and had that same user try again so I could look more carefully. The cookie domain and path looked right to me — it was set for our TaskFlow domain. I did see it had SameSite=Strict on it, if that matters? I don't really know what that does honestly. I also saw Secure was on there. I don't think we changed any cookie settings when we switched from Okta — we just updated the SSO provider config and the redirect URIs. So whatever cookie policy was there before is probably still the same. Good point about the incognito thing though, that does kind of fit with it being a cookie issue.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop after Okta-to-Entra SSO migration due to email claim mismatch and SameSite=Strict cookie policy","problem":"After migrating from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop when logging into TaskFlow. They authenticate successfully at Entra but are immediately redirected back to the login page. The remaining 70% of users can log in without issue.","root_cause_hypothesis":"Two compounding issues: (1) **Email claim mismatch:** TaskFlow's user lookup matches the email claim from the identity provider against stored user records. Entra normalizes emails differently than Okta — it strips plus-sign subaddressing (sending `jane@company.com` instead of `jane+taskflow@company.com`) and sends the current primary email for aliased accounts (which may differ from the original signup email in TaskFlow). When TaskFlow can't find a matching user record, it treats the user as unauthenticated and redirects back to login. (2) **SameSite=Strict session cookie:** The auth session cookie is set with `SameSite=Strict`, which causes browsers to omit the cookie on cross-site navigations — including the redirect back from Entra to TaskFlow. This means even if the user record match succeeds, the session may not persist across the redirect, restarting the auth flow. This may explain why incognito windows intermittently work (clean cookie jar, no stale Okta session cookies interfering).","reproduction_steps":["Have a user whose TaskFlow account email contains a plus-sign subaddress (e.g., jane+taskflow@company.com) attempt to log in via Entra SSO","User authenticates successfully at Microsoft","Observe the redirect back to TaskFlow — the email claim in the token is jane@company.com (plus-sign stripped)","TaskFlow fails to match this email to the stored user record and redirects back to login","The redirect loop repeats 3-4 times rapidly","Same behavior is reproducible for users whose Entra primary email differs from their original TaskFlow signup email due to aliasing"],"environment":"TaskFlow with Microsoft Entra ID (recently migrated from Okta). Affects Chrome and Edge equally. Session cookie has SameSite=Strict and Secure attributes. Redirect URIs verified correct in Entra app registration. Affected users can authenticate to other Entra-backed apps without issue.","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. These users include anyone with plus-sign email subaddressing and users whose emails were changed or aliased during the Entra migration. They are fully blocked from using the application.","recommended_fix":"**Primary fix — email matching:** Change TaskFlow's user lookup during SSO callback to match on a stable, identity-provider-agnostic identifier rather than raw email string comparison. Options: (a) match on the `oid` (object ID) or `sub` claim from the Entra token, which is stable per-user; (b) normalize emails before comparison by stripping plus-sign subaddresses and resolving aliases; (c) add a lookup fallback that tries the base email (without plus-sign) if the exact match fails. Also consider a one-time migration to reconcile stored emails with Entra's canonical emails. **Secondary fix — cookie policy:** Change the session cookie from `SameSite=Strict` to `SameSite=Lax`. `Lax` still protects against CSRF on sub-requests but allows the cookie to be sent on top-level navigations (like the redirect back from Entra), which is required for OAuth/OIDC redirect flows to work correctly.","proposed_test_case":"1. Create test users with plus-sign emails (user+tag@domain.com) and verify they can authenticate when Entra returns the base email without the plus-sign. 2. Create test users whose stored email differs from their Entra primary email (simulating alias scenarios) and verify authentication succeeds. 3. Verify the session cookie is set with SameSite=Lax and persists across the cross-origin redirect from Entra back to TaskFlow. 4. Verify that after the fix, a full login-redirect-callback cycle completes without looping.","information_gaps":["Have not directly confirmed the token claims for aliased users (strong inference only, based on how Entra handles aliases)","Unknown whether SameSite=Strict actually caused issues with Okta or if Okta's flow avoided cross-site navigation differently","TaskFlow's exact user lookup implementation (field matched, database schema) is unknown — the fix assumes email-based matching","Unknown whether there are additional affected users beyond the plus-sign and alias categories who may be hitting only the SameSite issue"]}

---
