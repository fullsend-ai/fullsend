# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the redirect loop behavior is clear. To help narrow down reproduction, could you share whether there's any pattern you've noticed among the 30% of affected users? For example: are they using a specific browser, were their accounts migrated differently, do they have different roles or group memberships in Entra ID, or were they perhaps already logged into TaskFlow with active sessions from the old Okta setup when the switch happened?

---

### reporter

Good question. I haven't noticed a browser pattern — we have people on Chrome, Edge, and Firefox all affected. As for the migration, I think the common thread might be email-related? A bunch of our team uses plus-addressing for their emails (like jane+taskflow@company.com) and I think most of the affected users fall into that group. We also had a few people whose emails changed or were aliased when we set up Entra ID. I'm not 100% sure that's the exact pattern but it's what I've noticed so far. I don't think roles or groups are the issue since some affected users are regular members and some are admins. And we did clear out the old Okta sessions before switching, so I don't think that's it either.

---

### triage-agent

Thanks for identifying the plus-addressing pattern — that's a really useful lead. To round out the picture, could you share some environment details? Specifically: what version of TaskFlow are you running, what authentication library or middleware does your deployment use for SSO (e.g., passport-azure-ad, next-auth, Spring Security SAML, etc.), and is TaskFlow deployed on-premises or in the cloud? These details will help a developer narrow down where the email parsing might be going wrong during the token-to-session handoff.

---

### reporter

We're on TaskFlow v2.3.1, self-hosted on our own infrastructure. As for the authentication library or middleware, I'm honestly not sure — I'm more on the IT admin side than the dev side, so I don't know the specifics of how the SSO integration is implemented under the hood. I just configured the Entra ID app registration and redirect URIs. I could maybe dig into the config files if you tell me what to look for, but I wouldn't know offhand what library it's using.

---

### triage-agent

That's all really helpful, thank you — no worries about the auth library, a developer can figure that out from the codebase.

One more thing that would help narrow this down: could you (or one of the affected users) check for any error output during a failed login attempt? Specifically:

- **Browser developer console** (F12 → Console tab): any errors or warnings that appear during the redirect loop?
- **Server-side logs**: if you have access to TaskFlow's application logs on your self-hosted instance, are there any errors or warnings logged when an affected user tries to log in?

Even a partial snippet would help — we're looking for anything related to session creation, token validation, or email/user matching that might confirm the plus-addressing theory.

---

### reporter

I had one of the affected users open dev tools while trying to log in. I didn't see any obvious errors in the console tab, but I did notice something in the Network tab — there's a Set-Cookie header coming back on the redirect from Microsoft, but the cookie doesn't seem to actually stick? Like, on the next request back to TaskFlow it's not there. I'm not a frontend dev so I'm not sure if I'm reading that right, but it seemed odd. I haven't dug into the server-side logs yet — I can check those but where would they typically be on a self-hosted install? I'll look in the morning if you can point me in the right direction.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra-ID migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. They authenticate successfully with Microsoft but are immediately redirected back to the identity provider upon returning to TaskFlow. The pattern correlates with users who have plus-addressing in their email addresses (e.g., jane+taskflow@company.com) or whose emails were aliased during the Entra ID setup. A network-tab observation shows that the Set-Cookie header from the auth callback response does not persist on the subsequent request.","root_cause_hypothesis":"The session or auth cookie value is derived from or includes the user's email address. The '+' character in plus-addressed emails is likely being improperly encoded or rejected during cookie creation or session lookup, causing the session cookie to either fail to be set (malformed Set-Cookie header) or fail to match on the next request. This would cause TaskFlow to treat the user as unauthenticated on the callback, restarting the OAuth flow. Okta may have normalized or stripped the plus-addressing before returning the email claim, masking this bug, while Entra ID preserves it.","reproduction_steps":["Configure TaskFlow v2.3.1 SSO with Microsoft Entra ID","Create or use a user account whose email contains plus-addressing (e.g., user+tag@company.com)","Attempt to log in to TaskFlow via SSO","Observe: after successful Microsoft authentication, the redirect back to TaskFlow triggers another redirect to Microsoft, creating an infinite loop","Compare with a user whose email has no special characters — login succeeds normally"],"environment":"TaskFlow v2.3.1, self-hosted on-premises, Microsoft Entra ID as SSO provider, multiple browsers affected (Chrome, Edge, Firefox)","severity":"high","impact":"~30% of the team cannot log in to TaskFlow at all. These users are completely blocked from accessing the application. Workarounds (if any) are unknown. The issue spans roles — both regular members and admins are affected.","recommended_fix":"1. Inspect the SSO callback handler where the email claim from the OIDC/SAML token is used to create or look up a user session. 2. Check whether the '+' character (or other special characters) in the email is being URL-encoded, stripped, or otherwise mangled before being written into the session cookie or session store. 3. Check the Set-Cookie header construction — ensure the cookie value is properly encoded and the '+' character is not causing a malformed header that the browser silently rejects. 4. Compare the email claim format returned by Entra ID vs. what Okta returned — Entra ID may preserve plus-addressing while Okta normalized it. 5. Fix email handling to properly encode or normalize emails consistently across the auth flow.","proposed_test_case":"Write an integration test that performs the SSO callback flow with an email containing plus-addressing (e.g., test+tag@example.com). Assert that: (a) the session cookie is set correctly in the response, (b) the cookie is accepted and matched on the subsequent authenticated request, and (c) the user is not redirected back to the identity provider.","information_gaps":["Exact authentication library/middleware in use (developer can determine from codebase)","Server-side logs from a failed login attempt (developer can pull these during investigation)","Whether the issue also affects users with other special characters in emails beyond '+'"]}

---
