# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — that redirect loop pattern is a helpful clue.

I'd like to understand what sets the affected ~30% apart from everyone else. Have you noticed any pattern among the users who get stuck? For instance:

- Were the affected users already logged into TaskFlow (with active sessions) at the time of the SSO switch, while the others weren't?
- Do they share anything in common — same browser, same OS, same office/VPN, same role or group membership in your identity provider?

Even a hunch here would help narrow things down — sometimes the pattern isn't obvious until you start looking.

---

### reporter

Good question. I did some digging on that and I don't think it's related to active sessions — we actually had everyone log out before the switch. It's not browser-specific either, the affected people hit the loop in Chrome, Edge, and Firefox.

What I did notice is that a lot of the affected users have a '+' in their email address. We use plus addressing for some folks, like jane+taskflow@company.com. And there are a few others who had their email changed or aliased when we moved to Entra — like they originally signed up with one email and then their primary email in Entra is slightly different. The users who can log in fine all seem to have straightforward email addresses that haven't changed. I'm not 100% sure that's the pattern but it's the best I've come up with so far.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, but TaskFlow immediately redirects them back to Microsoft. The loop repeats indefinitely. Affected users can log into other Entra-backed apps without issue.","root_cause_hypothesis":"TaskFlow's SSO callback handler fails to match the authenticated user's identity from the Entra ID token to a local TaskFlow account. Two likely sub-causes: (1) Plus-addressed emails (e.g., jane+taskflow@company.com) — the '+' character may be URL-decoded as a space during the callback, or Entra may normalize/strip the plus-address portion, causing a mismatch against the stored email. (2) Email aliases — users whose primary email changed during the Entra migration have a different email in the OIDC/SAML token than what TaskFlow has stored from the Okta era. In both cases, the lookup fails, no session is created, and TaskFlow treats the user as unauthenticated, restarting the login flow.","reproduction_steps":["Configure TaskFlow SSO with Microsoft Entra ID","Create or use a test user whose email contains '+' (e.g., testuser+tag@company.com) or whose Entra primary email differs from the email stored in TaskFlow","Attempt to log in to TaskFlow via SSO","Observe: authentication at Microsoft succeeds, redirect back to TaskFlow occurs, but TaskFlow immediately redirects back to Microsoft, creating an infinite loop"],"environment":"TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers (Chrome, Edge, Firefox). Server-side issue, not client-specific.","severity":"high","impact":"~30% of the team is completely locked out of TaskFlow. No workaround available for affected users short of changing their email addresses.","recommended_fix":"Investigate the SSO callback handler's user-matching logic. Specifically: (1) Check how the email claim from the Entra ID token is compared against stored user emails — ensure '+' characters are preserved and properly handled (not URL-decoded to spaces). (2) Add support for matching on multiple email attributes or aliases, or add a migration step that updates stored emails to match Entra primary emails. (3) Consider matching on a stable, provider-agnostic identifier (like a user GUID or immutable username) rather than email alone, to prevent this class of issue during future identity provider changes. Add logging in the SSO callback to record the received email claim and the lookup result so matching failures are visible.","proposed_test_case":"Write integration tests for the SSO callback user-matching logic that cover: (a) a user with a plus-addressed email authenticates and is correctly matched, (b) a user whose stored email differs from their Entra primary email is correctly matched via alias or secondary lookup, (c) when no match is found, the user receives a clear error message instead of a redirect loop.","information_gaps":["Which SSO protocol TaskFlow uses (OIDC vs SAML) and which specific claim/assertion carries the email","Whether TaskFlow's user lookup is email-based or uses another identifier","Exact server-side logs from a failed login attempt showing the received token claims and lookup query","Whether the few affected users without '+' in their email all fall into the 'email changed during migration' category"]}

---

