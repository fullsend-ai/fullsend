# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the redirect loop after an SSO migration is a clear and actionable description.

To help narrow this down: have you noticed any pattern that distinguishes the ~30% of affected users from those who can log in successfully? For example:

- Were the affected users' accounts provisioned differently (e.g., created under Okta vs. manually)?
- Do they belong to a different group or role in Entra ID?
- Are they using a different browser, or do they have existing TaskFlow sessions/cookies that predate the switch?

Any pattern you've spotted (or explicitly ruled out) would be very helpful.

---

### reporter

Good question — I've been trying to figure out the pattern myself. One thing I noticed is that a lot of the affected users have those 'plus' email addresses, like jane+taskflow@company.com. We set those up a while back so people could filter TaskFlow notifications. Not all affected users have that though — a couple of them had their email addresses changed at some point, like they joined under one email and we later updated it in our directory.

I don't think it's a browser thing, we've seen it on Chrome, Edge, and Firefox. I did tell a few people to try clearing their cookies and that seemed to help sometimes — one person said logging in from an incognito window worked, which was weird. But it's not consistent.

---

### triage-agent

Thanks for digging into the pattern — the plus-address and changed-email observations are really useful clues.

To help us understand the environment: could you share a few details about your TaskFlow deployment?

- Which SSO protocol are you using — SAML 2.0 or OpenID Connect (OIDC)?
- What version of TaskFlow are you running, and is it self-hosted or cloud-hosted?
- Do you see anything relevant in the TaskFlow server logs when an affected user attempts to log in? (Even a generic error or a repeated auth callback entry would help.)

---

### reporter

I'm pretty sure we're using OIDC — that's what we had set up with Okta and we kept the same protocol when we moved to Entra. We're on TaskFlow v2.3.1, self-hosted on our own infrastructure.

As for server logs, honestly I haven't dug into those much yet. I'm more on the admin side than the dev side. I can take a look but I'm not sure exactly where to find the relevant auth logs. Is there a specific log file or section I should be looking at?

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop after Okta-to-Entra migration for users with plus-addresses or changed emails","problem":"After migrating SSO from Okta to Microsoft Entra ID (OIDC), approximately 30% of users experience an infinite redirect loop when logging in. They authenticate successfully with Entra but TaskFlow fails to match their session and redirects them back to the IdP. The issue correlates with users who have plus-format email addresses (e.g., jane+taskflow@company.com) or whose email addresses were changed in the directory at some point. Clearing cookies or using incognito mode sometimes resolves it temporarily.","root_cause_hypothesis":"TaskFlow is likely matching the OIDC identity token's email or subject claim against a stored user record, and the match fails for affected users. Entra ID may normalize or strip the plus-address suffix (returning jane@company.com instead of jane+taskflow@company.com), or it may return a different email claim than Okta did for users whose emails were changed. When the lookup fails, TaskFlow doesn't create a session and redirects back to the IdP, creating the loop. The intermittent cookie-clearing fix suggests stale Okta session cookies may also interfere in some cases.","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with OIDC against Microsoft Entra ID","Ensure a user account exists in TaskFlow with a plus-format email (e.g., jane+taskflow@company.com)","Have that user attempt to log in via SSO","Observe: user authenticates with Entra successfully, is redirected back to TaskFlow, and is immediately redirected to Entra again in a loop"],"environment":"TaskFlow v2.3.1, self-hosted, OIDC protocol, Microsoft Entra ID as IdP (migrated from Okta). Observed on Chrome, Edge, and Firefox.","severity":"high","impact":"~30% of the team cannot log into TaskFlow at all. These users are completely blocked from using the application. Workarounds (incognito mode, clearing cookies) are inconsistent.","recommended_fix":"1. Check how TaskFlow resolves the OIDC identity token to a local user record — inspect whether it matches on the `email` claim, the `sub` claim, or another field. 2. Compare the email/sub values in the Entra ID token against what's stored in TaskFlow's user table for affected users (look for plus-address stripping or email mismatches). 3. If matching on `email`, consider switching to the stable `sub` (subject) claim, or add email normalization logic that handles plus-addressing. 4. For users with changed emails, ensure the stored identifier matches what Entra returns. 5. Investigate whether stale Okta session cookies are interfering — consider invalidating all existing sessions post-migration.","proposed_test_case":"Create test users with plus-format emails and users whose emails have been updated. Perform OIDC login flow and verify that the identity token's claims correctly resolve to the right local user record without triggering a redirect loop. Additionally, test login with pre-existing session cookies from the old IdP to confirm they're handled gracefully.","information_gaps":["Server-side auth logs showing the exact failure point in the redirect loop (reporter is willing to check but unsure where to find them)","The exact OIDC claims (email, sub, preferred_username) that Entra ID returns for affected vs. unaffected users","Whether TaskFlow's user lookup uses the email claim, sub claim, or another identifier"]}

---
