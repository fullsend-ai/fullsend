# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up. The fact that only ~30% of users are affected is a strong clue — do you notice any pattern among the affected users? For example:

1. **They had active/remembered sessions in TaskFlow before the SSO switch** (i.e., they didn't clear cookies or were logged in during the migration)
2. **Their accounts differ in some way** — e.g., they have email aliases, are in different Entra ID groups, or were provisioned differently than the working users
3. **It's browser-specific** — affected users are on a particular browser, or it works in incognito/private browsing
4. **Something else you've noticed**

If you haven't checked yet, a quick test would be: can an affected user log in successfully using an incognito/private browser window?

---

### reporter

Good questions. So I haven't noticed a browser-specific pattern — we have people on Chrome, Edge, and Firefox all hitting the issue. But now that you mention it, I did have one user try incognito and she said it worked that time, though I'm not sure if it works consistently.

As for the pattern among affected users — I think a lot of them are people who use plus-addressed emails, like jane+taskflow@company.com. We had a few people set up that way. And I also think some of the affected users had their email addresses changed or aliased when we migrated to Entra ID — like they had one email in Okta and their primary in Entra is slightly different. But I'm not 100% sure that accounts for all of them.

I don't think it's related to active sessions because we actually told everyone to log out before the migration. Though I guess not everyone listens to IT emails, ha.

---

### triage-agent

Thanks — this is really helpful and I think I'm zeroing in on the cause. Let me propose a hypothesis:

TaskFlow likely matches the authenticated user by comparing an email claim from the SSO token against what it has stored in its user database. After the migration, two groups of users are breaking:

1. **Plus-addressed users** (e.g., `jane+taskflow@company.com`) — Entra ID may be returning just `jane@company.com` in the token, so TaskFlow can't find a matching account and kicks them back to login.
2. **Users whose primary email changed** between Okta and Entra — same mismatch, different reason.

The incognito fix your user saw may have been a coincidence, or there could also be a stale Okta session cookie contributing.

Can you check one thing to confirm this? In TaskFlow's admin panel or database, do the affected users' stored email addresses match what Entra ID shows as their primary email — or do you see mismatches (plus-addressing, aliases, different domains, etc.)?

---

### reporter

Yeah, I can check that. So looking at a few of the affected users — you're right, there are mismatches. Like Jane's account in TaskFlow is stored as jane+taskflow@company.com but Entra ID is sending jane@company.com. And for a couple others, their emails were different between the two systems. So that does seem to line up.

Though I will say, I'm not totally sure the incognito thing was a coincidence — the user who tried it said it worked on her first attempt but then when she tried again later in incognito it didn't work. So something weird is going on with cookies too maybe? I don't know, I'm not a developer, but it feels like there might be more than just the email thing going on.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users whose stored email doesn't match Entra ID email claim","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite login redirect loop. They authenticate successfully with Entra ID but TaskFlow fails to match them to an existing account and sends them back to login. Affected users have email mismatches between what TaskFlow has stored and what Entra ID returns in the token.","root_cause_hypothesis":"TaskFlow performs an exact-match lookup of the email claim from the SSO token against its user database. Users with plus-addressed emails (e.g., jane+taskflow@company.com stored in TaskFlow, but jane@company.com returned by Entra ID) or users whose primary email changed during the Okta-to-Entra migration fail this lookup. When no matching user is found, TaskFlow likely redirects back to the login flow, creating the loop. A secondary contributor may be stale Okta session or OIDC state cookies that interfere with the new Entra ID flow.","reproduction_steps":["Identify a user whose email in TaskFlow's database differs from their Entra ID primary email (e.g., plus-addressed or aliased)","Have that user attempt to log into TaskFlow via SSO","Observe successful Entra ID authentication followed by redirect back to TaskFlow","TaskFlow fails to find a matching user and redirects back to Entra ID, creating an infinite loop"],"environment":"TaskFlow instance recently migrated from Okta to Microsoft Entra ID SSO. Multiple browsers affected (Chrome, Edge, Firefox). Not browser-specific.","severity":"high","impact":"Approximately 30% of the team is completely locked out of TaskFlow. These users cannot log in at all through normal means.","recommended_fix":"1. **Immediate fix:** Update affected users' stored emails in TaskFlow's database to match their Entra ID primary email, or add email aliases so both forms resolve. 2. **Code fix:** Normalize email comparison in the SSO callback — strip plus-addressing before lookup, or match on a stable claim like `oid` (Entra object ID) or `sub` instead of email alone. 3. **Defensive improvement:** When SSO authentication succeeds but no user match is found, show a clear error page instead of redirecting back to login. 4. **Investigate cookies:** Clear any Okta-specific session/OIDC cookies that may still be set, and verify the OIDC state parameter handling in the new Entra ID flow.","proposed_test_case":"Create a test user in TaskFlow with a plus-addressed email (user+tag@domain.com). Configure the SSO mock/test IdP to return user@domain.com as the email claim. Verify that login succeeds and resolves to the correct user. Also verify that when no user match is found, the system shows an error page rather than looping.","information_gaps":["Exact breakdown of how many affected users are plus-addressed vs. changed-email vs. other","Whether stale Okta cookies are a contributing factor for some users (inconsistent incognito results suggest possible secondary issue)","Which specific claim field TaskFlow uses for user matching (email, preferred_username, sub, etc.)"]}

---
