# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. To help narrow this down: have you noticed any pattern among the 30% of users who are stuck in the loop? For example, do they share anything in common — like they were using specific browsers, had existing TaskFlow sessions open during the SSO switch, belong to certain Entra ID groups or have different role assignments, or perhaps their accounts were provisioned differently in Okta vs. the others? Also, does clearing cookies and browser storage for your TaskFlow domain resolve the loop for any of them?

---

### reporter

Good question — I did notice a pattern actually. The affected users tend to be ones who use plus-addressing in their email (like jane+taskflow@company.com) or people whose email changed at some point and we set up aliases in Entra ID. The users who can log in fine are the ones whose straightforward primary email is the same as what they had in Okta.

As for clearing cookies — sometimes it works briefly. Like if they clear all cookies and try again right away, they might get in, but it's not consistent. A couple people told me that incognito/private browsing mode sometimes works when their normal browser doesn't, which is weird. But it's not a reliable fix either way.

---

### triage-agent

That's a really helpful pattern — the plus-addressing and alias connection is a strong lead. Could you check one more thing? If you look at TaskFlow's authentication logs (or the Entra ID sign-in logs under Enterprise Applications), can you compare what email or identifier Entra ID is sending back in the token for an affected user vs. a working user? Specifically, I'm curious whether Entra ID is returning the user's primary email (e.g., `jane@company.com`) while TaskFlow has them stored under the plus-address (`jane+taskflow@company.com`) — or vice versa. Also, do you know which claim TaskFlow is configured to use for user lookup — is it `email`, `preferred_username`, `upn`, or something else?

---

### reporter

I checked the Entra ID sign-in logs and yeah, you're right on the money. For an affected user like Jane who was set up as jane+taskflow@company.com in Okta, Entra ID is sending back just jane@company.com as the email. So there's definitely a mismatch there. For the users who work fine, the email in the token matches what TaskFlow has on file.

As for which claim TaskFlow uses — honestly I'm not sure, I'd have to dig into the config. We're on TaskFlow v2.3.1 self-hosted if that helps narrow it down. I assume it's the email claim but I haven't looked at the SAML/OIDC config in detail.

One thing that's bugging me though — even if the email mismatch explains *which* users are affected, I'm not sure it explains the redirect *loop* specifically. Like, wouldn't a mismatch just give an error message? Instead the browser just goes back and forth endlessly between Entra and TaskFlow. I noticed in the browser dev tools that TaskFlow does seem to set a cookie on the redirect back, but then on the next request the cookie isn't there. Not sure if that's related or just a symptom.

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop for users whose TaskFlow email doesn't match Entra ID primary email (plus-addressing / alias mismatch)","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. These users authenticate successfully with Entra ID but are immediately redirected back to the IdP instead of landing in TaskFlow. The affected users are those whose email stored in TaskFlow (often a plus-address like jane+taskflow@company.com, or a legacy alias) differs from the primary email Entra ID returns in the token (jane@company.com).","root_cause_hypothesis":"TaskFlow performs user lookup using the email claim from the OIDC/SAML token. When Entra ID returns the user's primary email but TaskFlow has the user stored under a plus-address or alias, the lookup fails. Instead of returning an authentication error, TaskFlow's auth middleware fails to establish a valid session — the session cookie is either not set or immediately invalidated — which causes the middleware to treat the user as unauthenticated on the next request and restart the SSO flow, creating the loop. The cookie-then-no-cookie behavior observed in browser dev tools confirms the session is not being persisted after the failed user lookup.","reproduction_steps":["Deploy TaskFlow v2.3.1 self-hosted with Entra ID as the SSO provider","Create a user in TaskFlow whose stored email is a plus-address (e.g., jane+taskflow@company.com)","Ensure the corresponding Entra ID account's primary email is the base address (jane@company.com)","Attempt to log in as that user via SSO","Observe the infinite redirect loop between TaskFlow and Entra ID","Compare with a user whose TaskFlow email matches their Entra ID primary email — login works normally"],"environment":"TaskFlow v2.3.1, self-hosted. SSO provider: Microsoft Entra ID (migrated from Okta). Affects users with plus-addressing or email aliases where stored email differs from Entra ID primary email.","severity":"high","impact":"Approximately 30% of the team is unable to reliably log into TaskFlow. No consistent workaround exists — clearing cookies and incognito mode are unreliable. This blocks daily work for affected users.","recommended_fix":"1. Identify which claim TaskFlow uses for user lookup (likely `email` — check the OIDC/SAML config). 2. Fix the user lookup to handle the mismatch — either normalize emails by stripping plus-addressing before lookup, or add a secondary lookup by alias/alternate email. 3. Fix the silent failure path: when user lookup fails after successful IdP authentication, TaskFlow should display an error ('Account not found for email jane@company.com') rather than failing to set a session and restarting the auth flow — this is the direct cause of the loop. 4. As an immediate mitigation, update affected users' emails in TaskFlow to match their Entra ID primary email. 5. Consider adding a configurable claim mapping so admins can choose which Entra ID claim to match against.","proposed_test_case":"Create integration tests for the SSO callback handler: (a) user with matching email logs in successfully, (b) user with plus-address mismatch receives a clear error message instead of a redirect loop, (c) if alias/normalized lookup is implemented, user with plus-address is matched correctly to their account. Also add a unit test verifying the session is established (or an error is raised) after the IdP callback — never a silent no-op.","information_gaps":["Exact claim TaskFlow is configured to use for user lookup (likely email, but not confirmed)","Whether TaskFlow's auth handler has any logging for failed user lookups that could confirm this diagnosis","Whether the session cookie issue is a separate bug in cookie handling (e.g., SameSite or Secure flag misconfiguration) or purely a consequence of the failed user lookup"],"clarity_scores":{"symptom":0.9,"cause":0.75,"reproduction":0.8,"impact":0.85,"overall_ambiguity":0.17}}

---

