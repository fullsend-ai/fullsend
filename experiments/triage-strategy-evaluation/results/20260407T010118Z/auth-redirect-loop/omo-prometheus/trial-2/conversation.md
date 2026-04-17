# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop pattern is a classic SSO integration headache.

To start narrowing this down: have you identified any pattern that distinguishes the ~30% of affected users from those who can log in fine? Specifically:

- Were the affected users migrated differently in Entra ID (e.g., created fresh vs. synced from Okta)?
- Do the affected users belong to a different security group, have a different domain suffix on their email, or have any other account attribute that differs from the working users?

Knowing what's different about that 30% will help us zero in on the root cause quickly.

---

### reporter

Good question. I've been looking into this and I think there might be a pattern with email addresses. A bunch of the affected users have those plus-sign email addresses — like jane+taskflow@company.com — we use plus addressing for some people to route things. And I think a few others had their email addresses changed at some point, like they were originally under a different email in our old system and we set up aliases when we moved to Entra. The users who work fine seem to just have straightforward email addresses that haven't changed. I'm not 100% sure that accounts for all of the affected users but that's what I've noticed so far.

---

### triage-agent

Thanks — that plus-addressing and alias pattern is a really strong lead.

Next I need to understand what's happening on TaskFlow's side during the loop. Can you check the TaskFlow application logs for one of the affected users during a login attempt? Specifically I'm looking for:

1. Does TaskFlow receive the callback from Entra ID at all (i.e., does the auth callback endpoint get hit)?
2. If so, are there any error messages — particularly anything about user not found, email mismatch, or session creation failure?
3. Do you know what claim TaskFlow uses to identify users — is it the `email` claim, `preferred_username`, `sub`, or the UPN (`upn`)?

If you don't have easy access to logs, even a browser dev tools Network tab showing the redirect chain (the sequence of URLs and HTTP status codes during the loop) would help narrow this down.

---

### reporter

I'm not really a developer so I haven't dug into the application logs much, but I did open the browser dev tools for one of the affected users like you suggested. I can see the redirect chain — they hit TaskFlow, get sent to Microsoft login, authenticate fine (200 OK from Microsoft), then get redirected back to TaskFlow's callback URL, and then TaskFlow immediately sends them back to Microsoft again. It just keeps looping.

One thing I did notice in the Network tab is that there's a Set-Cookie header in the response when they get redirected back from Entra, but I'm not sure the cookie is actually sticking? Like on the next request to TaskFlow it doesn't seem to be there. I don't know if that's relevant or not.

As for what claim TaskFlow uses, I'm honestly not sure — I'd have to check with whoever set up the original Okta integration. I think it's probably the email claim but I couldn't say for certain.

---

### triage-agent

That cookie observation is really helpful — it's likely the key to why the loop happens. Here's my working theory: TaskFlow receives the authentication from Entra, but when it tries to match the incoming user to an existing TaskFlow account, the match fails for affected users, so no valid session gets created, and they get bounced back to login.

To test this theory, could you check one thing for me? For one or two of the affected users (especially someone with a plus-sign address like `jane+taskflow@company.com`):

1. What email address is listed in their **TaskFlow user profile/settings**?
2. What email address is listed as their primary email in **Entra ID**?

I'm specifically looking for whether these two values are identical or differ in any way — even small differences like the plus-sign portion being present in one system but not the other, or a different domain/alias being used.

---

### reporter

OK so I just checked for one of our plus-address users. In TaskFlow her profile shows jane+taskflow@company.com, which is what she was set up with originally when we were on Okta. But in Entra ID, her primary email is just jane@company.com — the plus part isn't there. Entra seems to be sending just the base email without the plus alias. I haven't checked all the affected users but I'd bet it's similar for the other plus-address folks. For the ones with changed emails, I think it's a similar kind of mismatch — their Entra email is different from what's stored in TaskFlow. I can check a specific one of those too if it would help.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop caused by email mismatch between TaskFlow user records and Entra ID email claim","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop during login. They authenticate successfully with Entra but TaskFlow never establishes a session, sending them back to Entra immediately. Affected users fall into two groups: (1) users with plus-sign email addresses (e.g., jane+taskflow@company.com) where Entra strips the plus portion, and (2) users whose email addresses changed during the migration and now have aliases.","root_cause_hypothesis":"TaskFlow's auth callback uses the email claim from the OIDC token to look up the corresponding local user account. Entra ID returns the user's primary email (e.g., jane@company.com), but TaskFlow has the user stored under a different email (e.g., jane+taskflow@company.com or a pre-migration address). The lookup fails, no session is created (explaining the cookie that doesn't stick), and TaskFlow's auth middleware sees an unauthenticated request and redirects back to the IdP, creating the loop.","reproduction_steps":["Ensure a TaskFlow user account exists with a plus-sign email (e.g., jane+taskflow@company.com) or a legacy email address that differs from their Entra ID primary email","Configure TaskFlow SSO to use Microsoft Entra ID where the user's primary email is jane@company.com (no plus portion)","Attempt to log in as that user","Observe: successful Entra authentication, redirect to TaskFlow callback, immediate redirect back to Entra, infinite loop"],"environment":"TaskFlow with SSO via Microsoft Entra ID (recently migrated from Okta). Affects users whose TaskFlow-stored email differs from their Entra ID primary email claim. Other Entra-integrated apps work fine for these users.","severity":"high","impact":"Approximately 30% of the team is completely locked out of TaskFlow with no known workaround. These users cannot access the application at all.","recommended_fix":"Investigate the user lookup logic in TaskFlow's OIDC/OAuth callback handler. The fix likely involves one or more of: (1) Normalizing email comparison — strip plus-addressing before lookup, or use case-insensitive/alias-aware matching. (2) Switching the identity claim from `email` to a stable identifier like `sub` (OIDC subject) or `oid` (Entra object ID) and storing that mapping during migration. (3) As an immediate remediation, provide an admin tool or script to bulk-update TaskFlow user emails to match their Entra ID primary emails. Also verify that the auth callback handles lookup failures gracefully (return an error page rather than silently redirecting to login).","proposed_test_case":"Create a test user in TaskFlow with email 'testuser+alias@company.com'. Configure the OIDC mock/test IdP to return 'testuser@company.com' as the email claim. Verify that the user can authenticate successfully and a session is created. Additionally, test with a user whose stored email is a completely different alias (pre-migration address) to ensure the lookup still resolves correctly.","information_gaps":["Exact OIDC claim TaskFlow uses for user lookup (likely `email` but unconfirmed — reporter needs to check with the original developer)","Whether the changed-email/alias users exhibit the exact same mismatch pattern (reporter strongly suspects so but only verified one plus-address case)","Whether TaskFlow logs an explicit error during the failed lookup or fails silently"]}

---
