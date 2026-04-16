# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the redirect loop behavior and the 30% affected rate are really helpful starting points.

To help narrow this down: have you noticed any pattern that distinguishes the affected users from those who can log in successfully? For example:
- Were the affected users' accounts provisioned differently (e.g., created under Okta vs. created fresh in Entra ID)?
- Do affected users belong to a different group, role, or organizational unit?
- Does the same user always get the loop, or is it intermittent for a given person?

Any pattern here would help us build reliable reproduction steps.

---

### reporter

Good question. It's consistent — the same users hit the loop every time, it's not random. I haven't fully figured out the pattern yet but I did notice that a few of the affected users use plus-addressed emails (like jane+taskflow@company.com). Also, at least a couple of them had their email changed or aliased at some point when we were setting up Entra — like their primary email in Entra might be slightly different from what they originally signed up with in TaskFlow under Okta. The users who work fine seem to have straightforward emails that match up. I'm not 100% sure that accounts for all the affected users though. Oh and one thing — a couple people said they were able to get in using an incognito window, but that didn't work for everyone.

---

### triage-agent

Thanks for investigating the email pattern — that's a really strong lead, especially the plus-addressed emails and the Entra-vs-TaskFlow email mismatches.

Could you share some environment details so we can pin down the right area of the codebase?

- What version of TaskFlow are you running (or is this the hosted/SaaS version)?
- Which browsers are the affected users on? (Particularly curious whether the incognito workaround correlated with a specific browser.)
- What authentication library or protocol is TaskFlow using for SSO — SAML, OIDC, or something else?

This will help us determine whether the issue is in session/cookie handling, the token validation layer, or the user-matching logic.

---

### reporter

We're on TaskFlow v2.3.1, self-hosted. The affected users are spread across Chrome, Edge, and Firefox so I don't think it's browser-specific. I'm not sure about the incognito thing correlating with a particular browser — I only heard about it from a couple people and didn't dig into it. As for the authentication protocol, I'm not really sure honestly — I just configured it according to the TaskFlow SSO docs for Entra ID. I think it's OIDC but don't quote me on that, I'm more of an IT admin than a developer.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with email mismatches between Entra ID and TaskFlow","problem":"After migrating from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when authenticating via SSO. They authenticate successfully with Microsoft but are immediately redirected back to the login flow upon returning to TaskFlow. The issue is consistent per-user, not intermittent.","root_cause_hypothesis":"TaskFlow's user-matching logic during the OIDC callback likely performs a strict email comparison between the email claim in the Entra ID token and the email stored in TaskFlow's user database. Users whose Entra primary email differs from their TaskFlow email — due to plus-addressing (jane+taskflow@company.com), email changes during migration, or alias differences — fail the lookup. The app cannot find a matching local user, so it does not establish a session and redirects back to the IdP, creating the loop. The incognito workaround for some users may indicate a stale session cookie or cached auth state is compounding the issue for a subset.","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with Entra ID SSO (OIDC)","Create a user in TaskFlow with a plus-addressed email (e.g., jane+taskflow@company.com) or an email that differs from their Entra ID primary email","Attempt to log in as that user via SSO","Observe that after successful Entra authentication, the user is redirected back to TaskFlow and immediately sent back to the Entra login page in a loop"],"environment":"TaskFlow v2.3.1, self-hosted, OIDC (likely), Microsoft Entra ID as IdP, multiple browsers (Chrome, Edge, Firefox)","severity":"high","impact":"~30% of the team is completely unable to log into TaskFlow. This blocks all access to the application for affected users with no reliable workaround.","recommended_fix":"Investigate the OIDC callback handler's user-matching logic. Check whether the email claim from the Entra ID token is being compared to the stored TaskFlow email using a strict exact match. The fix should normalize emails before comparison (strip plus-addressing, case-insensitive match) and/or match on a more stable identifier such as the OIDC `sub` claim or UPN. Also check whether the app falls through silently on user-not-found instead of showing an error, which would explain the redirect loop rather than an error page.","proposed_test_case":"Create test users with: (a) a plus-addressed email, (b) an email that differs between IdP and local DB by alias, (c) a straightforward matching email. Assert that all three can authenticate via SSO and are correctly matched to their local accounts. Additionally, verify that a failed user lookup produces an error message rather than a redirect loop.","information_gaps":["Server-side logs from TaskFlow during a failed login attempt (would confirm the exact failure point)","Whether the incognito workaround correlates with a specific browser or scenario","Exact authentication protocol configuration (OIDC vs SAML) — reporter believes OIDC but is unsure","Whether all affected users fit the email mismatch pattern or if there are other factors"]}

---
