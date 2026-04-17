# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop sounds frustrating, especially with only a subset of users hit.

I'd love to understand the pattern behind who's affected. Have you noticed anything that distinguishes the ~30% who get the loop from those who don't? For example — are they in different departments or security groups, using different browsers, or did some of them have existing TaskFlow sessions open during the SSO switch while others were logged out? Any pattern you've spotted (or ruled out) would be really helpful.

---

### reporter

Good question. I've been trying to figure out the pattern myself. It doesn't seem to be browser-related — we've seen it on Chrome, Edge, and Firefox. And it's not department-specific either.

One thing I did notice is that several of the affected users have those plus-sign email addresses, like jane+taskflow@company.com. We use plus addressing for some people to help with email filtering. I'm not 100% sure that's the only factor though, because I think there are a couple affected users who don't have the plus sign but might have had their email changed at some point — like they were under a different email in our old Okta setup.

Also, I noticed that sometimes if an affected user tries in an incognito window, it occasionally works on the first try, which is weird. But then in their normal browser it keeps looping.

---

### triage-agent

That's a really useful observation on both fronts — the plus-sign emails and the incognito behavior.

The incognito detail is especially interesting because it suggests something stored in the browser (like a session cookie or cached token from the old Okta setup) might be part of the problem. When you did the SSO switch, did you do anything to invalidate existing TaskFlow sessions — like clearing server-side sessions or revoking old tokens? Or were users expected to just naturally re-authenticate against the new provider?

And on the email front — when those plus-sign users authenticate through Entra ID, does the email claim coming back match exactly what Okta used to send? For example, is it possible Okta sent `jane+taskflow@company.com` but Entra ID sends `jane@company.com` (or vice versa)? If you're not sure, checking the token claims in your Entra ID logs for one of the affected users would tell the story.

---

### reporter

Hmm, good questions. For the session invalidation — I don't think we did anything specific to clear existing sessions. We just updated the SSO config to point to Entra ID instead of Okta and figured users would just re-authenticate naturally. Maybe that was a mistake?

On the email claim thing — honestly I'm not sure exactly what's in the tokens. I'm more of an IT admin than a developer, so I haven't dug into the actual token claims. But your theory makes sense — it's very possible Entra is sending back the primary email without the plus part. I could try to check the Entra ID sign-in logs if you tell me what to look for.

One other thing I forgot to mention — a couple users said that if they clear all their cookies and try again really quickly, they can sometimes get in. But if they wait or try again later it goes back to the loop. Not sure if that's relevant.

---

### triage-agent

Thanks — the 'works briefly after clearing cookies then breaks again' detail actually fits together with everything else. Here's the picture I'm forming, and I'd love you to check one thing to confirm or blow it up:

I think two things are colliding. First, when you switched SSO providers, existing sessions from Okta were left in place — so some users' browsers are presenting a stale Okta session cookie to TaskFlow, which then can't validate it against Entra ID, triggering a re-auth loop. Second — and I suspect this is the bigger factor — Entra ID is likely sending back a *normalized* email in its token (e.g., `jane@company.com`) while TaskFlow has those users stored with their plus-sign address (`jane+taskflow@company.com`). TaskFlow can't match the incoming identity to a user record, so it kicks them back to authenticate again, creating the loop. The reason incognito occasionally works on the first try is that it bypasses the stale cookie, but the email mismatch still eventually catches up.

Could you check the Entra ID sign-in logs for one of the affected plus-sign users? In the Azure portal, go to **Entra ID → Users → Sign-in logs**, find a recent login attempt for someone like `jane+taskflow@company.com`, click into it, and look at the **Basic info** or **Authentication Details** tab — specifically the `email` or `preferred_username` claim value. Does it show the full plus-sign address, or just the base email? That one detail would confirm whether this is the root cause.

---

### reporter

OK, I'll try to check that. Give me a few minutes...

Alright, I went into the Azure portal and found the sign-in logs for one of our affected users, Jane. Her account in our system is jane+taskflow@company.com, but in the sign-in log details, the email claim just shows jane@company.com — so yeah, it looks like Entra is stripping the plus part. That lines up with what you were thinking.

Your theory about the stale cookies makes some sense too, but I'm not totally sure that's the whole story — even brand new users we onboarded after the switch (who never had an Okta session) are hitting the loop if they have plus addresses. So it might be more about the cookie being set during the redirect itself? I remember one of our developers glanced at the browser dev tools and mentioned something about the cookie not sticking on the redirect back from Microsoft, but I didn't fully understand what they meant.

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop for users whose Entra ID email claim doesn't match stored TaskFlow email","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but TaskFlow cannot match the returned identity to a local user record, so it never establishes a session and redirects back to the IdP repeatedly.","root_cause_hypothesis":"TaskFlow's user-lookup during the OAuth/OIDC callback matches the email claim from the identity provider against stored user records. Entra ID normalizes email addresses by stripping the sub-addressing (plus-sign) portion — so a user stored as `jane+taskflow@company.com` receives an email claim of `jane@company.com`. The lookup fails, no session is created, and the auth middleware redirects back to Entra ID, creating the loop. Users whose emails changed between the Okta and Entra configurations are affected for the same reason (stored email ≠ incoming claim). Stale Okta session cookies may contribute in some cases but are not the primary cause, since brand-new users with plus addresses also experience the loop.","reproduction_steps":["Have a user account in TaskFlow with a plus-sign email address (e.g., jane+taskflow@company.com)","Configure TaskFlow SSO to use Microsoft Entra ID","Ensure the Entra ID user's primary email is jane@company.com (no plus-sign portion)","Attempt to log in to TaskFlow via SSO","Observe: successful Entra ID authentication, redirect back to TaskFlow, immediate redirect back to Entra ID, repeating indefinitely"],"environment":"TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Affects multiple browsers (Chrome, Edge, Firefox). Specific TaskFlow version not reported but issue is server-side user-lookup logic, not browser-specific.","severity":"high","impact":"~30% of the team cannot log in to TaskFlow at all. These are users with plus-sign email addresses or users whose email changed during the SSO migration. Complete loss of access for affected users with no reliable workaround.","recommended_fix":"1. **Immediate fix:** Investigate the user-lookup logic in the OAuth/OIDC callback handler. Identify where the incoming email claim is matched against stored user records and add case-insensitive, sub-address-aware matching (normalize both sides by stripping the `+...` portion before the `@` for comparison purposes, or match on a stable claim like `sub`/`oid` rather than email). 2. **Session cleanup:** Invalidate all existing pre-migration sessions to eliminate stale Okta session cookies as a contributing factor. 3. **Entra ID configuration:** Alternatively or additionally, configure optional claims in the Entra ID app registration to emit the full email with plus-addressing, or map a custom claim. 4. **Data reconciliation:** For users whose email changed between Okta and Entra, update the stored email or add a secondary identifier lookup.","proposed_test_case":"Create a test user in TaskFlow with email `testuser+tag@company.com`. Configure the mock/test IdP to return the email claim as `testuser@company.com`. Verify that the OAuth callback successfully matches the user, creates a valid session, and redirects to the dashboard without looping. Also verify that a user with a matching email (no plus-sign discrepancy) continues to work correctly.","information_gaps":["Exact TaskFlow version and auth library/framework in use (needed to locate the specific callback code)","Whether TaskFlow uses the email claim, preferred_username, sub, or oid as the primary user-lookup key","What the developer saw in browser dev tools regarding 'the cookie not sticking' — likely a symptom of the failed lookup but could reveal a secondary SameSite or Secure cookie-flag issue on the redirect","Whether any affected users have neither plus-sign addresses nor email changes (would indicate a broader matching issue)"]}

---
