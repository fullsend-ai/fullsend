# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — that's really helpful context.

I'd like to start by understanding the divide between affected and unaffected users. Have you noticed anything that the ~30% who are stuck in the loop have in common? For example:

- Were they migrated differently, or at a different time?
- Do they tend to use a particular browser, or have existing TaskFlow sessions open during the switch?
- Do they belong to specific groups or roles within your org?

Even a hunch or partial pattern would be useful here — sometimes the distinguishing factor isn't obvious at first.

---

### reporter

Hmm, good question. I haven't noticed a browser pattern — we have people on Chrome and Edge affected, and people on both of those who are fine too. They were all migrated at the same time.

One thing I did notice is that a few of the affected users have those plus-sign email addresses, like jane+taskflow@company.com. We use that for some people to route emails. But I'm not sure if that accounts for all of them. There might also be some users whose emails changed at some point — like we had a couple people who were set up in TaskFlow with a personal email originally and then we aliased them in Entra later. I think some of those people are affected too.

Oh, and one weird thing — a couple users said they could get in using an incognito window sometimes, but not always. Not sure if that helps.

---

### triage-agent

That's a really interesting pattern — the plus-sign emails and the changed-email users both point in a similar direction. It sounds like there might be a mismatch between the email address Entra ID is sending back in its token and the email TaskFlow has stored for these users.

Would you be able to pick one or two affected users and compare what's in their TaskFlow profile (the email TaskFlow knows them by) against what Entra ID is actually returning in the authentication token? If you can inspect the SAML assertion or OIDC token claims from Entra — even just the `email` or `preferred_username` claim — that would tell us a lot. For example, is Entra sending `jane@company.com` while TaskFlow has `jane+taskflow@company.com`, or vice versa?

Also, for the users whose emails changed over time — does TaskFlow still have their original personal email on record, or was it updated to match their company identity in Entra?

---

### reporter

I'm not super technical with the token stuff, but I can check what's in TaskFlow's user profiles. Let me look... yeah, so for one of the plus-sign users, TaskFlow has them as jane+taskflow@company.com but I'm pretty sure Entra is sending back just jane@company.com. That would make sense because Entra treats the plus part as an alias I think.

For the email-change users, TaskFlow still has their old personal email on file — we never got around to updating those in TaskFlow since it was working fine with Okta. I assume Entra is sending their company email which wouldn't match what TaskFlow has.

I don't really know how to inspect the actual SAML tokens or whatever though. Is there an easier way to confirm this? Also I'm still confused about the incognito thing — if it's just an email mismatch, why would incognito sometimes work?

---

### triage-agent

That comparison is really valuable — it confirms the email mismatch theory. For the token inspection, don't worry about digging into SAML directly. What you just did (comparing the TaskFlow profile email against what Entra knows the user as) tells us what we need.

The incognito question is a great one, and I want to follow that thread because it might mean there's a second thing going on. When the affected users log in normally (not incognito), does TaskFlow set any kind of "remember me" or persistent session cookie? What I'm wondering is whether this is happening:

1. User authenticates with Entra, which sends back `jane@company.com`
2. TaskFlow can't match that to `jane+taskflow@company.com`, so instead of showing an error, it silently redirects back to the SSO provider to try again — creating the loop
3. But in incognito, there's no pre-existing TaskFlow session cookie, so the app might be hitting a slightly different code path (maybe a first-time login flow that's more forgiving about matching)

Does that match what you're seeing? Specifically — when it *does* work in incognito, are those users prompted to set up their account as if they're new, or does it seem to recognize them as existing users?

---

### reporter

Hmm, I'd have to double-check with the users who got it working in incognito. From what I remember, I think at least one person said it seemed like it was treating them as a new user — like they had to go through some initial setup again. But honestly it's been inconsistent, sometimes incognito works and sometimes it doesn't even then.

Your theory about the redirect loop makes sense for the email mismatch users, but I'm not sure it fully explains it. Like, I would expect an error message saying 'account not found' or something, not an infinite loop. It's almost like TaskFlow is trying to set something up on the redirect back but it's not sticking, so it just keeps trying. I noticed in the browser address bar that the URL flickers back and forth really fast between our TaskFlow URL and the Microsoft login page.

Also I should mention — one of my coworkers who's more technical looked at the browser dev tools briefly and said something about a cookie being set in the response but then not actually appearing in the browser afterward? I didn't fully understand what he meant but maybe that's relevant to the incognito thing.

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop after Okta-to-Entra ID migration due to email mismatch and session cookie not persisting","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are trapped in an infinite redirect loop: they authenticate successfully with Entra, get redirected back to TaskFlow, and are immediately sent back to Entra. The remaining 70% of users log in normally. Affected users include those with plus-sign email aliases (e.g., jane+taskflow@company.com) and users whose TaskFlow profile still has a stale personal email that differs from their Entra identity.","root_cause_hypothesis":"Two interacting issues: (1) TaskFlow's SSO callback handler performs an email-based user lookup, and when Entra ID returns a normalized email (e.g., jane@company.com) that doesn't match what TaskFlow has stored (e.g., jane+taskflow@company.com or a legacy personal email), the lookup fails. (2) Instead of rendering an error on lookup failure, TaskFlow attempts to establish a session and redirect — but the session cookie is not persisting in the browser (likely a SameSite, Secure flag, or Domain attribute misconfiguration introduced or exposed by the SSO URL change). Since the session cookie doesn't stick, the next page load sees an unauthenticated user and redirects to Entra again, creating the loop. The incognito inconsistency is explained by incognito sometimes hitting a first-time-user code path that bypasses the strict email match, but still being subject to the same cookie persistence issue.","reproduction_steps":["Identify a user whose TaskFlow profile email differs from their Entra ID email (plus-sign alias or legacy personal email)","Have that user attempt to log into TaskFlow via the Entra ID SSO flow","Observe the redirect loop in the browser — URL flickers between TaskFlow and Microsoft login","Open browser dev tools Network tab and observe: (a) Entra redirects back with a valid auth code/token, (b) TaskFlow sets a Set-Cookie header in its response, (c) the cookie does not appear in the browser's cookie store on subsequent requests","Compare with a user whose TaskFlow email matches their Entra email exactly — they log in successfully"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected (Chrome, Edge). Issue is not browser-specific. Entra ID app registration and redirect URIs verified as correct.","severity":"high","impact":"~30% of users are completely unable to log into TaskFlow. This includes users with plus-sign email aliases and users with stale email records. Workarounds (incognito) are unreliable.","recommended_fix":"Investigate two things in order: (1) **Cookie persistence** — Inspect the Set-Cookie headers TaskFlow sends after the SSO callback. Check for SameSite=None without the Secure flag, incorrect Domain attribute, or a cookie path that changed with the SSO migration. The SSO provider URL change from Okta to Entra may have affected cross-site cookie behavior. This is likely the direct cause of the loop vs. a clean error. (2) **Email matching logic** — The SSO callback handler should normalize emails before lookup (strip plus-sign suffixes, case-insensitive comparison) or match on a stable identifier like the OIDC `sub` claim rather than email alone. For the legacy-email users, either update their TaskFlow profiles or implement a secondary lookup by Entra's `oid` or `sub` claim. Also add a failure path that surfaces a clear error message (e.g., 'No account found for this email') instead of silently redirecting back to the SSO provider.","proposed_test_case":"Create test users with (a) a plus-sign email alias that differs from the IdP-returned email, (b) an email that doesn't match the IdP-returned email at all, and (c) an exactly matching email. Verify that (a) and (b) either match successfully after normalization or display a clear error — never enter a redirect loop. Separately, write an integration test that asserts the session cookie Set-Cookie header has correct SameSite, Secure, and Domain attributes for the production domain.","information_gaps":["Exact cookie attributes (SameSite, Secure, Domain, Path) being set by TaskFlow — needs developer inspection of the Set-Cookie headers","Whether TaskFlow uses SAML or OIDC with Entra (affects which claims are available for matching)","Whether the cookie issue also affects the 70% who can log in (they may be getting lucky with email matches masking the cookie problem)","Full list of affected users to confirm the email-mismatch correlation covers all cases, not just the observed subset"]}

---
