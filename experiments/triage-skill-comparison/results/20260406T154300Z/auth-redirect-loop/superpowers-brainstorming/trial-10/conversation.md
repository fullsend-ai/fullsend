# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up. The 30% vs 70% split is a strong clue — have you noticed any pattern that distinguishes affected users from those who can log in fine? For example:

1. **Stale sessions** — Affected users had active/cached TaskFlow sessions from the Okta era (e.g. they didn't clear cookies), while unaffected users hadn't logged in recently or used a different browser
2. **Group or role differences** — Affected users belong to different Entra ID groups, have different role assignments, or were migrated differently from Okta
3. **Claim differences** — Affected users have something different in their Entra ID profile (e.g. email format, UPN vs email mismatch, multi-domain)
4. **Something else**

Also — if an affected user tries logging in from an incognito/private window, does the loop still happen?

---

### reporter

Good questions. So a few things I've noticed:

For the pattern — it's not really about groups or roles. We looked into it and the common thread seems to be email-related. A bunch of our affected users use plus-addressed emails (like jane+taskflow@company.com), and a few others had their emails changed or aliased when we migrated to Entra ID. The users who can log in fine are the ones whose emails are straightforward and haven't changed. So I think it's something about how the emails are set up between the two systems.

As for incognito — funny you mention that. A couple of affected users told me they were sometimes able to get in using an incognito window, though it doesn't work every time. Clearing all cookies and trying again quickly also sometimes works, but it's not reliable. That makes me think there's some kind of cookie or session issue on top of whatever the email thing is.

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop caused by email claim mismatch after Okta-to-Entra-ID migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Entra ID, get redirected back to TaskFlow, but TaskFlow does not establish a session and immediately redirects them back to Entra ID. The affected population correlates with users who have plus-addressed emails (e.g. jane+taskflow@company.com) or whose email addresses were changed/aliased during the migration. Clearing cookies or using incognito sometimes breaks the loop, but not reliably.","root_cause_hypothesis":"TaskFlow's OIDC callback handler matches the incoming identity token's email claim against stored user records. Entra ID likely returns a different claim value than Okta did for these users — common scenarios: (1) Entra ID returns the UPN (jane@company.com) instead of the primary email, stripping the plus-address tag; (2) Entra ID returns a new primary alias rather than the legacy email stored in TaskFlow's user table. When the lookup fails, TaskFlow never creates an authenticated session, so the auth middleware redirects back to the IdP, creating the loop. The secondary cookie/incognito factor suggests that stale Okta-era session or CSRF cookies may also interfere with the new auth flow, causing the loop even for some users whose email would otherwise match.","reproduction_steps":["Create or identify a test user in Entra ID with a plus-addressed email (e.g. testuser+taskflow@company.com)","Ensure TaskFlow's user record stores that plus-addressed email","Attempt to log in to TaskFlow via SSO","Observe the redirect loop: Entra ID authenticates successfully, but TaskFlow redirects back to Entra ID repeatedly","Inspect the OIDC id_token claims returned by Entra ID (especially 'email', 'preferred_username', and 'upn') and compare against the stored user email in TaskFlow's database","Optionally reproduce the cookie variant: log in as a normal user, then swap to an affected user without clearing cookies"],"environment":"TaskFlow with OIDC/SSO integration, recently migrated from Okta to Microsoft Entra ID. Affects roughly 30% of the user base. Affected users can authenticate to other Entra ID-connected apps without issue, confirming the problem is in TaskFlow's token handling, not Entra ID itself.","severity":"high","impact":"~30% of the team is unable to reliably log into TaskFlow, blocking their daily work. Workarounds (incognito, cookie clearing) are unreliable.","recommended_fix":"1. **Inspect the token claims**: Log the full decoded id_token at TaskFlow's OIDC callback endpoint and compare the email-related claims (`email`, `preferred_username`, `upn`) for affected vs. unaffected users. Determine which claim Entra ID populates and whether it matches what TaskFlow looks up. 2. **Fix the user-matching logic**: Update the callback handler to normalize emails before lookup (strip plus-address tags, case-normalize, or match against known aliases). Consider matching on a stable immutable identifier (Entra ID `oid` or `sub` claim) rather than email. 3. **Clear stale session state**: On authentication callback, invalidate any pre-existing session cookies before establishing the new session to prevent stale Okta-era cookies from interfering. 4. **Backfill user records**: For users whose emails changed during migration, update or add email aliases in TaskFlow's user table so the lookup succeeds.","proposed_test_case":"Unit/integration test for the OIDC callback user-matching function: given an id_token with email claim 'jane@company.com', verify it matches a stored user record with email 'jane+taskflow@company.com'. Additionally, verify that a user with a pre-existing session cookie from a different IdP configuration can still complete the login flow without looping.","information_gaps":["Exact OIDC claim(s) TaskFlow uses for user lookup (email vs. sub vs. upn) — requires checking the codebase","Whether TaskFlow stores Okta-era subject identifiers that could be used as a migration bridge","Whether the Entra ID app registration's optional claims configuration includes the 'email' claim or only returns UPN by default"]}

---
