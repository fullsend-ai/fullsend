# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up. The partial failure pattern (30% affected) is a strong clue — do you notice anything the affected users have in common that the unaffected users don't? For example:

1. **They were logged into TaskFlow before the SSO switch** (i.e., they might have old session cookies from Okta)
2. **They use a different browser or device** than the unaffected users
3. **They belong to a different group or role** in your identity provider
4. **Something else**, or you're not sure yet

Also — if it's option 1, does clearing cookies/using an incognito window fix the loop for those users?

---

### reporter

Good question. I don't think it's browser-related — we've seen it on Chrome, Edge, and Firefox. And it's not a group/role thing either, I checked that already in Entra.

Clearing cookies and using incognito does sometimes help, weirdly enough. Not consistently though — some users say incognito worked once but then the loop came back when they tried in their normal browser.

As for what the affected users have in common... I'm not 100% sure, but I noticed that a lot of them are people who use plus-addressed emails, like jane+taskflow@company.com. We have a bunch of people who set that up for filtering purposes. I also think some of the affected users had their email addresses changed or aliased at some point — like they originally signed up with one email and then we added it as an alias in Entra. But I haven't fully confirmed that pattern across everyone who's affected.

---

### triage-agent

Thanks — the plus-addressing and email alias pattern is a really strong lead. Here's my working theory:

TaskFlow likely matches users by comparing the email in the SSO token to what it has stored in its user database. When you switched to Entra ID, the email claim in the token may not match what TaskFlow has on file — for example:

- Entra might send `jane@company.com` while TaskFlow has `jane+taskflow@company.com` stored
- Or for aliased users, Entra sends the primary email while TaskFlow has the old/alias email

This mismatch would explain the loop: authentication succeeds at Microsoft's end, but TaskFlow can't find the user, so it redirects back to login.

Could you check one thing to confirm? **Pick 2-3 affected users and compare the email stored in TaskFlow's user database to the `email` or `preferred_username` claim in their Entra ID token** (you can see this in Entra's sign-in logs under the token claims). Do they differ?

---

### reporter

That theory makes a lot of sense actually. I haven't dug into the token claims specifically, but I can check the Entra sign-in logs. I do know that for the plus-addressed users, their primary email in Entra is just the plain version without the plus part — so yeah, jane@company.com in Entra vs jane+taskflow@company.com in TaskFlow sounds right.

I'll try to pull that up for a couple of affected users and get back to you. Might take me a bit since I'm still getting used to the Entra admin console.

One thing I wanted to mention though — even if there's a mismatch, I'm a little confused about why incognito/clearing cookies sometimes helps temporarily. If it was purely an email mismatch, wouldn't it just fail every time? Unless something else is also going on with the session or cookies?

---

### triage-agent

Great catch on the incognito inconsistency — you're right that a pure email mismatch would fail every time. I think you may actually have **two overlapping issues**:

1. **Stale Okta cookies/session artifacts** — When you switched SSO providers, users who were previously logged in may still have OAuth state cookies, OIDC nonce cookies, or session tokens from the old Okta integration sitting in their browser. These could interfere with the new Entra flow, which would explain why clearing cookies sometimes helps.

2. **Email mismatch** (the plus-addressing issue) — This would cause a *persistent* failure even after stale cookies are cleared, which is why incognito doesn't consistently fix it for plus-addressed users.

To confirm: **when an affected user clears cookies and the loop comes back, is that user one of the plus-addressed/aliased email users?** In other words, does clearing cookies permanently fix the loop for affected users who *don't* have a plus-addressed or aliased email, while plus-addressed users still get stuck even after clearing?

---

### reporter

Hmm, that's a good question but I'm honestly not sure I can confirm that cleanly. I haven't tracked it that carefully on a per-user basis. What I can say is that even some users who don't use plus addressing have had the loop come back after clearing cookies — so I don't think clearing cookies is a permanent fix for anyone, really. It seems more like it works sometimes and then stops working again, regardless of the email situation. That's part of why it's been so frustrating to troubleshoot. But I'll try to pay closer attention to whether the plus-addressed users are the ones who never get relief from clearing cookies vs the others. I also noticed the UI looks a bit different after we switched but that's probably unrelated.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop after Okta→Entra ID SSO migration due to email mismatch and stale session state","problem":"After switching SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, and are immediately sent back to Microsoft. Clearing cookies/incognito provides inconsistent temporary relief. Affected users can log into other Entra-backed apps without issue.","root_cause_hypothesis":"Two overlapping issues: (1) TaskFlow's user-matching logic compares the email claim from the Entra ID token against its stored user records using an exact string match. Users with plus-addressed emails (e.g., jane+taskflow@company.com) or changed/aliased emails have a mismatch because Entra sends the canonical email (jane@company.com). Authentication succeeds at the IdP but TaskFlow can't resolve the user, triggering a redirect back to login. (2) Stale OAuth state cookies, OIDC nonce cookies, or session tokens from the old Okta integration persist in browsers and interfere with the new Entra auth flow, causing transient failures even for users without email mismatches and explaining why clearing cookies sometimes helps temporarily but the loop can recur as new broken session state is written.","reproduction_steps":["Have a user whose TaskFlow account email uses plus-addressing (e.g., jane+taskflow@company.com) or was changed/aliased after initial signup","Attempt to log into TaskFlow via the Entra ID SSO flow","Observe the user authenticates successfully at Microsoft but gets caught in an infinite redirect loop back to TaskFlow's login page","Clear all cookies and retry — loop may temporarily resolve but recurs in subsequent sessions"],"environment":"TaskFlow with SSO integration, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected (Chrome, Edge, Firefox). Entra ID app registration and redirect URIs verified correct by the reporter.","severity":"high","impact":"Approximately 30% of the team is locked out of TaskFlow entirely. The issue is persistent and not reliably resolved by user-side workarounds (clearing cookies). Productivity impact across the affected user base.","recommended_fix":"Investigate two areas: (1) **Email normalization in user matching** — Check how TaskFlow resolves the authenticated user after receiving the OIDC token from Entra. The matching logic should normalize emails by stripping plus-address suffixes and checking against known aliases, not just doing an exact string comparison. Compare the `email` and `preferred_username` claims in Entra sign-in logs against TaskFlow's user database for affected users to confirm the mismatch. (2) **Stale session cleanup** — Identify and clear any Okta-era cookies (OAuth state, OIDC nonce, session tokens) that TaskFlow's auth flow may still be reading. Consider adding a one-time migration step that invalidates all pre-migration sessions, or explicitly scoping new Entra session cookies so they don't collide with old Okta ones. Check server-side logs during a redirect loop to see whether TaskFlow is throwing a 'user not found' error, a session validation error, or something else — this will clarify which of the two issues is primary.","proposed_test_case":"Create test users with plus-addressed emails (user+tag@domain.com) and aliased emails, configure Entra to return the canonical (non-plus) email in the token, and verify that TaskFlow correctly resolves these users without entering a redirect loop. Additionally, simulate stale Okta session cookies in the browser and verify that the Entra auth flow completes successfully without interference.","information_gaps":["Server-side logs from TaskFlow during a redirect loop — would reveal the exact failure point (user lookup failure vs session error vs something else)","Exact token claims from Entra sign-in logs compared to TaskFlow user records for affected users — reporter plans to check but hasn't yet","Whether the 'UI looks different' observation indicates other changes were made to TaskFlow during the SSO migration","Whether TaskFlow stores the original email from initial signup or updates it — relevant to the alias mismatch theory"]}

---
