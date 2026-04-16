# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — this is a helpful starting point.

The 30% affected vs 70% unaffected split is the key clue here. Have you identified any pattern that distinguishes the affected users? Specifically:

- Were the affected users previously logged into TaskFlow with the old Okta SSO (i.e., could they have stale session cookies or cached tokens), while unaffected users hadn't logged in recently?
- Is there any difference in how the affected users' accounts are provisioned in Entra ID (e.g., migrated vs. newly created, different group memberships, different UPN/email domain)?

Knowing what's common among the affected group will help narrow this down quickly.

---

### reporter

Good question. I'm not sure about the stale session thing — we did ask people to clear cookies when we switched, but I doubt everyone actually did it. I do know that clearing cookies and trying again sometimes helps temporarily, but the loop usually comes back.

As for what's different about the affected users... now that I think about it, a few of them use plus-addressing in their emails, like jane+taskflow@company.com. And I think some of the others had their emails changed or aliased at some point during the migration. But I'm not 100% sure that accounts for all of them. The users who work fine seem to just have straightforward email addresses that were the same in both Okta and Entra.

---

### triage-agent

That's a really useful observation about the plus-addressing and aliased emails — I think we're closing in on this.

Can you check what identifier TaskFlow is using from the SSO token to match users? Specifically:

1. In TaskFlow's SAML/OIDC configuration, which claim is mapped as the user identifier — is it `email`, `preferred_username`, `upn`, `sub`, or something else?
2. Can you grab the SAML assertion or OIDC token for one affected user and one unaffected user and compare the value of that claim? For example, does Entra ID send `jane@company.com` while TaskFlow has `jane+taskflow@company.com` stored as their email?

This will tell us whether the redirect loop is caused by TaskFlow failing to match the incoming identity to a local account — which would explain why it bounces back to the IdP instead of completing the login.

---

### reporter

Honestly I'm not sure exactly which claim is mapped — I didn't set up the original SSO config, my predecessor did. I think it's using the email claim but I'd have to dig into the settings to confirm. I haven't looked at the actual tokens, but your theory makes sense — I bet Entra is sending back the primary email without the plus part, since Microsoft tends to normalize those. So jane+taskflow@company.com in TaskFlow's database would be getting jane@company.com back from Entra. I can try to check the token for one of the affected users, but I'm not really sure how to pull that up. Is there a way to see it in the browser dev tools or something?

---

### triage-agent

Your instinct about the email normalization is very likely correct — that's a well-known behavior with Microsoft Entra and plus-addressing.

Before we lock this in, could you check one thing? In TaskFlow's SSO/authentication settings, find the claim mapping configuration (it's usually in an admin panel under SSO, SAML, or OIDC settings). Look for which field is set as the **user identifier** or **NameID** — it'll be something like `email`, `preferred_username`, `upn`, `sub`, or `nameidentifier`. Let me know what you find.

Also, as a quick test: could you pick one affected user with a plus-address and temporarily change their email in TaskFlow's user database to the plain version (e.g., change `jane+taskflow@company.com` to `jane@company.com`)? If that user can then log in without the loop, we've confirmed the root cause and I can write up a clear fix path for the dev team.

For the token question — yes, you can see it in browser dev tools: open the Network tab, attempt the login, and look for the POST back to TaskFlow's callback URL. The token will be in the request body (usually as `SAMLResponse` or `id_token`). But the test above is faster and more conclusive.

---

### reporter

OK I'll try to dig into the SSO settings and find that claim mapping. I think I know where it is in the admin panel, give me a bit.

As for the email change test — that's a good idea, I can try that with one of our plus-address users. I'll let you know what happens.

One thing I did notice though, since you mentioned the dev tools — I had one of the affected users try logging in while I watched the Network tab. I could see a Set-Cookie header coming back on the redirect response from Entra, but the cookie didn't seem to actually stick. Like, the next request to TaskFlow didn't include it. I wasn't sure if that was related or just a browser thing. Does that mean anything?

---

### triage-agent

That cookie observation is actually very important — it could be a second issue compounding the email mismatch, or even the primary cause of the loop for some users.

While you're digging into the SSO settings and testing the email change, could you also check the cookie details from that Network tab capture? Specifically, look at the `Set-Cookie` header on the response that redirects back to TaskFlow and note:

1. **The `SameSite` attribute** — is it set to `Strict`, `Lax`, `None`, or not present at all? If it's `Strict` or missing, cookies won't be sent on cross-site redirects from Entra back to TaskFlow, which would cause exactly this loop.
2. **The `Secure` flag** — is it set? And is TaskFlow accessed over HTTPS? If `Secure` is set but you're on HTTP (even in a dev/staging environment), the cookie will be silently dropped.
3. **The `Domain` attribute** — does it match your TaskFlow domain exactly?

This matters because if the cookie isn't persisting, the redirect loop could happen even for users whose emails match perfectly — TaskFlow completes auth, tries to set a session cookie, the cookie gets dropped, so on the next page load TaskFlow sees no session and redirects back to the IdP. That would explain why clearing cookies only helps temporarily.

---

### reporter

I went back and looked at the Set-Cookie header from that session I captured. The SameSite attribute is set to `Strict` — I remember noticing that because I wasn't sure what it meant. I think the Secure flag is there too, and we are using HTTPS so that should be fine. I didn't pay close attention to the Domain attribute but I think it looked correct — it was our TaskFlow subdomain.

So you're saying the SameSite=Strict thing could be why the cookie isn't sticking? That would actually make a lot of sense because I also noticed that sometimes when users try in an incognito window it works on the first try, though I couldn't figure out why that would be different.

I'm still working on getting into the SSO claim mapping settings — I'll update you on that and the email change test when I have it.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop after Okta-to-Entra migration caused by SameSite=Strict session cookie and email claim mismatch","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Entra, get redirected back to TaskFlow, but are immediately sent back to Entra. The session is never established. Clearing cookies helps temporarily but the loop recurs.","root_cause_hypothesis":"Two compounding issues: (1) PRIMARY — TaskFlow's session cookie is set with SameSite=Strict, which causes browsers to drop the cookie on cross-site redirects from Entra back to TaskFlow. This means TaskFlow never sees the session cookie on the first post-auth request, treats the user as unauthenticated, and redirects them back to the IdP. This likely affects all users intermittently but some succeed due to browser-specific handling or navigation patterns (e.g., incognito works because there's no pre-existing cookie state interfering). (2) SECONDARY — For users with plus-addressed emails (jane+taskflow@company.com) or aliased/migrated emails, Entra ID likely returns a normalized email (jane@company.com) in the identity claim, which fails to match the stored email in TaskFlow's user database. Even if the cookie issue is fixed, these users would still fail authentication due to the identity mismatch.","reproduction_steps":["1. Configure TaskFlow SSO with Microsoft Entra ID (migrated from Okta)","2. Ensure TaskFlow's session cookie has SameSite=Strict","3. Have a user with a plus-addressed email (e.g., jane+taskflow@company.com) attempt to log in","4. User authenticates successfully at Entra ID","5. User is redirected back to TaskFlow's callback URL","6. Observe: Set-Cookie header is present in the response but the cookie is not included in the subsequent request (dropped due to SameSite=Strict on cross-site redirect)","7. TaskFlow sees no session, redirects back to Entra — loop begins"],"environment":"TaskFlow web application accessed over HTTPS, SSO via Microsoft Entra ID (recently migrated from Okta), session cookie configured with SameSite=Strict and Secure flag. Affects approximately 30% of users, with a correlation to plus-addressed or aliased email addresses.","severity":"high","impact":"~30% of the team is unable to log into TaskFlow reliably. No permanent workaround exists — clearing cookies provides only temporary relief. This blocks affected users from accessing the application entirely.","recommended_fix":"Two fixes needed, in priority order:\n\n1. **Change session cookie SameSite attribute from Strict to Lax.** SameSite=Lax permits cookies on top-level navigational redirects (like SSO callbacks) while still protecting against CSRF on sub-requests. This is the standard setting for applications using SSO. Find where the session cookie is configured (likely in the auth middleware or session config) and set SameSite=Lax. Do NOT set SameSite=None unless cross-origin iframe embedding is required, as None is more permissive than necessary.\n\n2. **Fix email claim matching.** Verify which OIDC/SAML claim is configured as the user identifier (check the SSO admin panel). If it's `email`, implement email normalization: strip plus-addressing suffixes before matching against the user database, or switch the identifier claim to a stable, non-email identifier like `sub` (OIDC subject) or `oid` (Entra Object ID). If sticking with email, also audit the user database for any accounts whose stored email doesn't match what Entra returns and reconcile them.","proposed_test_case":"1. **Cookie fix verification:** After changing SameSite to Lax, confirm that a previously-affected user can log in without redirect loop. Verify the session cookie is present in requests following the SSO callback redirect. Test in both normal and incognito browser sessions.\n\n2. **Email matching verification:** For a user with a plus-addressed email, confirm login succeeds after the fix. Verify by checking that the identity claim value from the token matches (or is normalized to match) the stored user record. Add a unit test for the email normalization/matching logic covering: plus-addressing, case differences, and aliased emails.","information_gaps":["Exact OIDC/SAML claim currently mapped as user identifier in TaskFlow (reporter is checking but hasn't confirmed yet)","Whether the email change test (removing plus-address) resolves the loop for a specific user (reporter is testing)","Whether the SameSite=Strict issue affects the 70% 'unaffected' users intermittently (they may succeed due to browser quirks but still be at risk)","Whether there are other cookie attributes (Domain, Path) that could cause issues for users on subdomains","The exact distribution of affected users between the cookie issue and the email mismatch issue"]}

---
