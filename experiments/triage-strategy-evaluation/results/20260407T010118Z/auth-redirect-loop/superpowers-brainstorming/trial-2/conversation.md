# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. The fact that only ~30% of users are affected is a really useful clue.

Do you notice any pattern that distinguishes affected users from those who can log in fine?

- **A.** Affected users had existing TaskFlow sessions/cookies from before the SSO switch
- **B.** Affected users are in a different group, role, or OU in Entra ID
- **C.** Affected users are on a specific browser or device type
- **D.** Affected users had accounts originally provisioned through Okta (vs. manually created)
- **E.** No obvious pattern — it seems random
- **F.** Something else

This will help narrow down whether it's a client-side state issue, a provisioning/claims mismatch, or something else entirely.

---

### reporter

Hmm, good question. It's not really A through E exactly. I've been looking at it and I think it might be F — something else. The users who are affected seem to be ones who use plus addressing in their email, like jane+taskflow@company.com. We have a bunch of people who set that up for filtering purposes. There might also be some people who had their email changed or aliased at some point, but I'm less sure about that pattern. The ones who log in fine seem to be using their straightforward email addresses. I don't think it's browser-specific since I've seen it happen on both Chrome and Edge.

---

### triage-agent

That's a really helpful observation — plus addressing is a strong lead.

Here's my working theory: when your users authenticate through Entra ID, the email claim in the token for plus-addressed users likely differs from what TaskFlow has stored in its user database. For example, Entra ID might return `jane@company.com` while TaskFlow has `jane+taskflow@company.com` on file (or vice versa). The user lookup fails, so TaskFlow doesn't establish a session and kicks the user back to the login flow, creating the loop.

Can you check one thing to confirm this? For an affected user, what does the email look like in **TaskFlow's user/account settings** — is it the plus-addressed form like `jane+taskflow@company.com`? And if you're able to inspect the Entra ID token or the user's profile in Entra ID, does the email there match or differ?

---

### reporter

Yeah, in TaskFlow the affected users have their plus-addressed email like jane+taskflow@company.com. I haven't dug into the Entra ID token directly — I'm not sure how to inspect that honestly — but in Entra ID's user profile, those users' primary email is just jane@company.com without the plus part. So there's definitely a mismatch there. One thing I wanted to mention though — even when some affected users try clearing their cookies and logging in again, it sometimes works briefly in an incognito window but then fails again in their regular browser. Not sure if that's related or a separate thing.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed emails after Okta-to-Entra ID SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. These users authenticate successfully with Entra ID but are never granted a TaskFlow session, causing the app to repeatedly redirect them back to the IdP. The affected users are those whose TaskFlow accounts were registered with plus-addressed emails (e.g., jane+taskflow@company.com).","root_cause_hypothesis":"TaskFlow's authentication callback performs an exact-match email lookup to find or create a user session. Entra ID's token returns the user's primary/canonical email (jane@company.com) without the plus-address suffix, while TaskFlow has the plus-addressed form (jane+taskflow@company.com) stored in its user database. The lookup fails, no session is established, and the user is redirected back to login — creating the loop. Okta likely preserved the plus-addressed form in its claims, which is why this only surfaced after the migration.","reproduction_steps":["Have a user account in TaskFlow registered with a plus-addressed email (e.g., jane+taskflow@company.com)","Configure the corresponding Entra ID user with primary email jane@company.com","Attempt to log into TaskFlow via SSO","Observe: user authenticates successfully at Entra ID, is redirected to TaskFlow callback, but is immediately redirected back to Entra ID in a loop"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects Chrome and Edge. Not browser-specific.","severity":"high","impact":"~30% of the team cannot log into TaskFlow at all. These are users who set up plus-addressed emails for filtering purposes. Complete login blockage for affected users with no reliable workaround.","recommended_fix":"1. Identify where TaskFlow matches the incoming SSO email claim against stored user emails (the auth callback/session creation code). 2. Normalize the comparison: strip the plus-address suffix (everything between + and @) before lookup, or implement case-insensitive matching on the base address. 3. Alternatively, configure the Entra ID app registration to emit a claim that includes the full plus-addressed email (e.g., using a claims mapping policy). 4. Also check which specific claim TaskFlow reads (email, preferred_username, upn) — Entra ID populates these differently and the plus-address may be available in one but not others. 5. Address the stale-cookie issue by ensuring failed auth attempts don't leave partial session state that blocks subsequent attempts.","proposed_test_case":"Create a test user with plus-addressed email (test+sub@example.com) in TaskFlow's user store. Mock an SSO callback with the canonical email (test@example.com) in the token claims. Verify that the user is matched correctly, a session is created, and no redirect loop occurs.","information_gaps":["Exact token claim TaskFlow reads for email matching (email vs. preferred_username vs. upn) — developer can inspect the auth code and token","Whether users with changed/aliased emails (non-plus-addressing) are also affected — same fix would apply","Whether Okta was configured to pass through plus-addressed emails or if TaskFlow's old auth code handled normalization"]}

---
