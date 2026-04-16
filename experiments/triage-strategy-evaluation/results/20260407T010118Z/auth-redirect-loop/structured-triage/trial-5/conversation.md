# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the redirect loop behavior is clear and helpful.

To help narrow down reproduction: have you noticed any pattern that distinguishes the ~30% of affected users from those who can log in fine? For example:
- Were the affected users' accounts originally provisioned through Okta, while unaffected users were created differently?
- Do affected users belong to different groups, roles, or OUs in Entra ID?
- Are they using the same browser/OS as the unaffected users?

Anything you've noticed (or ruled out) would help us reproduce this on our end.

---

### reporter

Good question — I've been trying to figure out the pattern myself. One thing I noticed is that a few of the affected users have those 'plus' email addresses, like jane+taskflow@company.com. We use plus addressing for some people to filter emails. But not all affected users have that, so I'm not sure if that's actually related.

I also think some of the affected users had their email addresses changed at some point — like they might have had a different email in Okta than what's now their primary in Entra. But I'd have to go back and check our records to be sure about that.

As far as browser/OS, it seems to happen across Chrome, Edge, and Firefox, so I don't think it's browser-specific. Oh, one weird thing though — a couple people said they got in using an incognito window, but it didn't work consistently.

---

### triage-agent

Thanks for digging into the user patterns — the plus-addressing and email-change observations are both really useful leads we can investigate.

Could you share some environment details about your TaskFlow deployment?

- What version of TaskFlow are you running?
- Is this a self-hosted deployment or our cloud/SaaS offering?
- If self-hosted, what's the server OS and how is TaskFlow deployed (Docker, Kubernetes, bare metal)?

This will help us check whether there are any known issues with the SSO handling in your version and narrow down where in the auth flow the loop is happening.

---

### reporter

We're on TaskFlow v2.3.1, and it's self-hosted. I'm not sure about the exact server setup off the top of my head — I think it's running in Docker but I'd have to check with our DevOps guy to be sure about the specifics. We're on a Linux server, Ubuntu I believe.

---

### triage-agent

Thanks for the version and hosting info — that's helpful.

One more thing that would really help us pinpoint where the loop is breaking: could you (or your DevOps colleague) grab the **server-side logs from TaskFlow** during one of these redirect loops? Specifically, any auth-related log entries or error messages that appear when an affected user tries to log in.

If TaskFlow is running in Docker, something like `docker logs <container_name>` filtered around the time of a failed login attempt would be ideal. We're looking for anything related to the SSO callback — token validation errors, session errors, or user-lookup failures that might explain why the app is rejecting the redirect and sending users back to Microsoft.

---

### reporter

I haven't had a chance to pull the server logs yet — I'll need to get with our DevOps person to do that, and he's out today. I can try to get those tomorrow.

One thing I can tell you from the browser side though — when I watched it happen on one of the affected user's machines, I opened up the browser dev tools and I could see that there was a Set-Cookie header coming back in the redirect response from Microsoft. So it looks like the server is trying to set the session cookie, but something is going wrong and the cookie doesn't seem to stick. By the time the page loads on TaskFlow's side, there's no session cookie, so it kicks them back to login again.

I'm not sure if that helps, but it seemed like a clue. I'll try to get the actual server logs as soon as I can.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop after Okta-to-Entra ID migration: session cookie not persisting for subset of users","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Microsoft but the session cookie set during the callback does not persist, causing TaskFlow to treat them as unauthenticated and redirect them back to Microsoft repeatedly.","root_cause_hypothesis":"The most likely cause is a user-identity mismatch between the Okta-era user records in TaskFlow's database and the claims/attributes returned by Entra ID. Users whose email changed during the migration, or who use plus-addressed emails (e.g., jane+taskflow@company.com), may fail a user-lookup or token-validation step during the SSO callback, preventing a valid session from being created. The server likely sets the cookie optimistically but then fails to associate it with a user, resulting in the redirect. The intermittent success in incognito windows suggests stale cookies or cached auth state from the old Okta flow may also be interfering.","reproduction_steps":["Set up TaskFlow v2.3.1 with Entra ID SSO configured","Attempt to log in as a user whose email was changed during the Okta-to-Entra migration, or who uses a plus-addressed email","Complete authentication on the Microsoft side","Observe redirect back to TaskFlow followed by immediate redirect back to Microsoft in a loop","Verify in browser dev tools that a Set-Cookie header is present in the callback response but the cookie does not persist on subsequent requests"],"environment":"TaskFlow v2.3.1, self-hosted, Docker on Ubuntu Linux, SSO via Microsoft Entra ID (recently migrated from Okta)","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. This is a login blocker with no reliable workaround (incognito works inconsistently).","recommended_fix":"Investigate the SSO callback handler in TaskFlow's auth module: (1) Check how the user lookup works during the OIDC/SAML callback — does it match on email claim, subject/nameID, or an internal ID? If it matches on email, plus-addressing normalization or email-change mismatches could cause lookup failures. (2) Check whether the session is actually created and persisted to the session store, or if an error during user lookup silently prevents session creation while still setting the cookie. (3) Check the SameSite and Secure attributes on the session cookie — the Entra ID redirect flow may differ from Okta's in a way that triggers browser cookie-rejection policies. (4) Consider adding a migration step or user-matching fallback that reconciles old Okta identities with new Entra ID claims.","proposed_test_case":"Create test users with plus-addressed emails and with emails that differ between the identity provider claim and the TaskFlow user record. Execute the SSO login flow and verify that (a) the user lookup succeeds and maps to the correct account, (b) a valid session is created and persisted, and (c) the session cookie is present and accepted on the post-redirect request to TaskFlow.","information_gaps":["Server-side logs from TaskFlow during a failed login attempt (reporter will provide once DevOps colleague is available)","Exact deployment details (Docker configuration, reverse proxy setup) — a reverse proxy stripping or misconfiguring cookie headers could also cause this","Confirmation of whether all affected users have email mismatches between old Okta records and current Entra ID claims"]}

---
