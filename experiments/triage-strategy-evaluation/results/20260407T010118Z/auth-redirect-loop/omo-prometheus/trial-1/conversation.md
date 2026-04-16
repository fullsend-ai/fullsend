# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop sounds frustrating.

To start narrowing this down: have you identified any pattern among the ~30% of affected users? Specifically:

- Did the affected users have existing active sessions in TaskFlow before the SSO switch, while unaffected users did not (or vice versa)?
- Are the affected users in a different group, role, or OU in Entra ID compared to those who can log in fine?

Knowing what distinguishes the two groups will help us zero in on the root cause quickly.

---

### reporter

Good question. I don't think it's related to existing sessions — we had everyone log out and clear cookies before the switch. And they're not in different groups or roles in Entra, we have a pretty flat setup.

What I have noticed is that a lot of the affected users are ones who use plus addressing in their email — like jane+taskflow@company.com. We had a few people set up that way. There are also a couple users who had their email addresses changed or aliased at some point, like they originally signed up with a personal email and we later linked their company email. I'm not 100% sure that accounts for all the affected users, but it's a pattern I noticed.

One other thing — a couple people said they were able to get in using an incognito window, but it doesn't always work.

---

### triage-agent

That plus-addressing pattern is a really strong lead — this is likely an email matching issue between what Entra ID sends back and what TaskFlow has stored for those users.

Can you check one thing for me? For one of the affected plus-addressing users (e.g. `jane+taskflow@company.com`), what email address does TaskFlow have stored in their user profile, and what email claim is Entra ID actually sending back in the token? You can check the latter by looking at the Entra ID sign-in logs for a failed login attempt — under the token claims, look for the `email` or `preferred_username` claim value.

I'm trying to determine whether Entra is stripping the plus tag (sending `jane@company.com`) while TaskFlow expects `jane+taskflow@company.com`, or the reverse.

---

### reporter

I'm not super familiar with digging into token claims, but I can check the Entra sign-in logs. Give me a bit... OK so I looked at Jane's sign-in log and it looks like the email claim coming back from Entra is just `jane@company.com` — without the +taskflow part. In TaskFlow her profile still shows `jane+taskflow@company.com`. So yeah, it looks like Entra is stripping the plus tag.

For the aliased users I'm less sure what's going on — I'd have to dig into that more. But the plus addressing thing seems like it's definitely a mismatch like you're describing.

---

### triage-agent

Great find — that confirms the plus-addressing mismatch is causing the loop for those users.

Before I wrap this up, I want to make sure we're not dealing with two separate bugs. For one of the aliased users — someone who originally signed up with a personal email and later linked their company email — can you check the same thing? Look at their TaskFlow profile email and compare it to the `email` claim in their Entra sign-in log.

Specifically, I'm trying to determine: is TaskFlow storing their old personal email while Entra is sending their company email, or is there some other mismatch? If it's the same class of problem (email in token ≠ email in TaskFlow), that tells us one fix covers both groups.

---

### reporter

OK so I checked on Mike, who's one of the aliased users. His TaskFlow profile shows his old personal email (mike.jones@gmail.com) but the Entra sign-in log shows his company email (mjones@company.com). So yeah, it looks like the same kind of mismatch — the email Entra sends doesn't match what TaskFlow has on file. I think when we set up his alias in Entra we made his company email the primary, so that's what gets sent in the token, but TaskFlow still has the old one stored. So it does seem like it's the same basic problem for both groups.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop caused by email mismatch between Entra ID token claims and TaskFlow user profiles","problem":"After migrating from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Entra but TaskFlow fails to match them to an existing account, causing a re-authentication redirect that loops indefinitely.","root_cause_hypothesis":"TaskFlow's SSO callback performs a strict email match between the `email` claim in the Entra ID token and the email stored in the TaskFlow user profile. This match fails for two groups: (1) users with plus-addressed emails (e.g. `jane+taskflow@company.com` in TaskFlow, but Entra sends `jane@company.com` — Entra strips the plus tag), and (2) users with aliased/changed emails (e.g. old personal email `mike.jones@gmail.com` in TaskFlow, but Entra sends their primary company email `mjones@company.com`). When the lookup fails, TaskFlow likely treats the user as unauthenticated and redirects back to the login flow, creating the loop.","reproduction_steps":["Have a user whose TaskFlow profile email differs from their Entra ID primary email (e.g. a plus-addressed email or a legacy personal email)","Attempt to log into TaskFlow via SSO","Authenticate successfully with Microsoft Entra ID","Observe redirect back to TaskFlow, which immediately redirects back to Entra, creating an infinite loop"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Entra ID app registration and redirect URIs confirmed correct. Flat group/role structure in Entra.","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. No reliable workaround exists (incognito mode works intermittently but not consistently). All affected users share the trait of having a stored email in TaskFlow that does not match the email claim Entra ID returns.","recommended_fix":"Investigate TaskFlow's SSO callback handler where the incoming email claim is matched against stored user profiles. The fix should address email normalization in at least two ways: (1) Normalize plus-addressed emails by stripping the +tag before comparison (or match on the base address), and (2) Support lookup by secondary/alias emails, not just the primary stored email. Consider adding an `sso_identifier` or `external_id` field tied to the Entra ID `oid` (object ID) claim rather than relying solely on email matching, which is fragile across IdP migrations. As an immediate remediation, update the affected users' emails in TaskFlow to match what Entra sends, or add the Entra email as a linked alias.","proposed_test_case":"Create test users with (a) a plus-addressed email stored in the profile, (b) a legacy email that differs from their IdP primary email. Simulate an SSO callback with the IdP returning the normalized/primary email. Verify that both users are correctly matched to their accounts and authenticated without redirect loops.","information_gaps":["Exact reason incognito windows intermittently work for some affected users (possibly cached tokens or session state, but not confirmed)","Whether there are any affected users who do NOT fall into the plus-addressing or aliased-email categories","Which specific code module handles the SSO callback and user lookup in TaskFlow"]}

---
