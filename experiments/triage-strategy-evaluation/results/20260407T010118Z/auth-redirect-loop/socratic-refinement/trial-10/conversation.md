# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that's a really helpful starting point.

I'm curious about the 30% of users who are affected: have you noticed anything that distinguishes them from the users who can log in successfully? For example, do they tend to be in a particular team or role, were their accounts created at a different time, or are they using different browsers or devices? Even a hunch would be useful here — sometimes the pattern isn't obvious until you start looking at it from a different angle.

---

### reporter

Good question. I've been trying to figure out a pattern myself. It's not really a team or role thing — we have affected users across different departments. They're using different browsers too (Chrome, Edge, Firefox). One thing I did notice is that several of the affected users have a '+' in their email address, like jane+taskflow@company.com — we use plus addressing for some people. But I'm not sure that's the whole picture because I think there are a couple affected users who don't have that. Some of the affected users also had their emails changed or aliased at some point during the Okta-to-Entra migration, but I'd have to double-check on that.

---

### triage-agent

That's a really interesting observation — the plus-addressing and the email changes during migration could definitely be related threads.

Can you tell me more about what happened with those email changes or aliases during the Okta-to-Entra migration? Specifically, I'm wondering whether the email address that Entra ID sends back in the login token might differ from what TaskFlow has stored for those users — for example, if someone was `jane+taskflow@company.com` in Okta but Entra knows them as `jane@company.com`, or vice versa. Have you had a chance to compare what Entra is returning in the authentication claims versus what TaskFlow expects for any of the affected users?

---

### reporter

Yeah, I think that's probably what's happening with the plus addressing users. We haven't dug into the actual token claims, but it would make sense that Entra is sending back the primary email without the plus part. In Okta we had it set up so the plus-addressed email was what got sent, but I'm not sure Entra handles that the same way.

For the aliased users, a couple people had their emails changed during the migration — like they were under an old domain or a different address in Okta, and we set up their new Entra account with a different primary email but added the old one as an alias. I could see how TaskFlow might still have the old email stored for them.

I haven't looked at the actual authentication claims directly — I'm not sure how to pull those up honestly. One thing I can say is that some affected users have been able to get in occasionally by using an incognito window or clearing all their cookies, which is weird if it's just an email mismatch issue. Not sure if that's related or a separate thing.

---

### triage-agent

That's really helpful context — I think we're closing in on this.

The incognito/cookie detail you mentioned is interesting and might point to a second layer of the problem. Can you tell me more about what happens in that scenario? Specifically: when an affected user clears their cookies and gets in successfully, does the problem come back the next time they log in, or does it stay fixed from that point on? And when they're stuck in the loop normally, does the URL change at all between redirects — for example, do you see any error parameters or session IDs appearing in the URL, or does it just bounce between the same two URLs endlessly?

I'm trying to understand whether the redirect loop is being driven purely by the identity mismatch, or whether TaskFlow might also be holding onto a stale session from the old Okta setup that's interfering with the new login flow.

---

### reporter

When clearing cookies works — and it doesn't always work, it's kind of hit or miss — it usually doesn't stick. Like they might get in that one time, but then next time they need to log in again they're back in the loop. So it's not like clearing a stale Okta session fixes it permanently.

As for the URLs, I haven't watched super carefully but it looks like it just bounces back and forth between TaskFlow's login page and the Microsoft login. I don't think I noticed error parameters but I wasn't really looking at the query string closely. It happens pretty fast — like the page just flickers back and forth.

I guess the weird thing to me is that if it were purely an email mismatch, I'd expect an error message like 'account not found' or something, not an infinite loop. It's like TaskFlow is trying to set up the session but then immediately deciding the user isn't logged in and sending them back. But I'm not a developer so I might be wrong about that.

---

### triage-agent

[RESOLVED] {"title":"Infinite redirect loop during SSO login when Entra ID email claim doesn't match stored user email","problem":"After migrating from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Entra but TaskFlow immediately redirects them back to the SSO provider instead of establishing a session. The loop is rapid and produces no user-facing error message.","root_cause_hypothesis":"TaskFlow's SSO callback handler looks up users by matching the email claim in the identity token against stored user records. When no match is found, instead of returning an error, the code treats the user as unauthenticated and redirects back to the SSO provider — which, seeing a valid session, immediately redirects back with the same token, creating the loop. The email mismatches have two sources: (1) Plus-addressed users (e.g. jane+taskflow@company.com) — Okta was configured to send the plus-addressed form, but Entra ID likely sends the primary address (jane@company.com). (2) Migrated/aliased users — their Entra primary email differs from the old email TaskFlow has stored from the Okta era. The intermittent cookie-clearing workaround may work in cases where it forces a fresh authentication flow that triggers a slightly different code path, but doesn't persist because the underlying email mismatch remains.","reproduction_steps":["Identify a user whose TaskFlow account email differs from their Entra ID primary email (e.g. a plus-addressed email or a pre-migration email alias)","Have that user attempt to log into TaskFlow via SSO","Observe the browser rapidly redirecting between TaskFlow's login endpoint and Microsoft's authentication endpoint in a loop","Optionally: compare by logging in as a user whose TaskFlow email matches their Entra ID primary email — login succeeds"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers (Chrome, Edge, Firefox). Approximately 30% of users affected. Affected users can authenticate to other Entra-integrated apps without issue.","severity":"high","impact":"~30% of the user base cannot reliably log into TaskFlow. Workarounds (clearing cookies, incognito) are unreliable and temporary. This is a complete blocker for affected users' daily work in the application.","recommended_fix":"1. **Immediate:** In the SSO callback handler, add an explicit error response when the email claim from the identity token doesn't match any stored user — return a clear error page (e.g. 'No account found for this email') instead of redirecting back to the SSO provider. This breaks the loop and makes the problem visible. 2. **Root cause fix:** Normalize email matching to be case-insensitive and plus-address-aware (strip the +suffix portion before lookup), OR update stored user emails to match what Entra ID returns, OR configure Entra ID app registration to send the plus-addressed email in the claim (via custom claims mapping). 3. **Migration cleanup:** Audit all user records to identify any remaining email mismatches between TaskFlow's stored emails and Entra ID primary emails, especially for users whose addresses changed during migration. Update TaskFlow records or add Entra aliases as needed.","proposed_test_case":"Create a test user in TaskFlow with the email 'testuser+alias@company.com'. Configure Entra ID to return 'testuser@company.com' as the email claim. Attempt SSO login. Verify that (a) the login either succeeds by matching the normalized email, or (b) a clear error message is shown — but never an infinite redirect loop.","information_gaps":["Exact email claim attribute Entra ID is configured to send (e.g. preferred_username, email, UPN) — a developer can check the Entra app registration and TaskFlow's OIDC config","Whether TaskFlow's auth flow has a JIT (just-in-time) provisioning or account-creation path that might be relevant","Server-side logs during the redirect loop — may reveal the specific lookup failure or session-establishment error","The exact list of affected users without plus-addressing or email changes — confirming whether other mismatch patterns exist beyond these two"]}

---
