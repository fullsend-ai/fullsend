# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. To help narrow this down: have you noticed any pattern that distinguishes the ~30% of affected users from those who can log in fine? For example:

1. **Account migration timing** — Were affected users' accounts migrated or provisioned differently in Entra ID?
2. **Browser/session state** — Does the issue resolve if affected users clear cookies or use an incognito window?
3. **Group or role differences** — Are affected users in different Entra ID groups, or do they have different roles/claims assigned?
4. **Something else** you've noticed

Any pattern here would really help us zero in on the cause.

---

### reporter

Good questions. On the browser/session side — yeah, a couple of affected users tried incognito and it seemed to work sometimes, which is weird. Clearing cookies also helped one person get in, but then the next day they were stuck again. So that's something but not a reliable fix.

As for a pattern among affected users, I've been trying to figure that out. I noticed that a few of the affected users have those plus-sign email addresses, like jane+taskflow@company.com — we use plus addressing for some accounts. But I'm not sure if that explains all of them. There might also be some users who had their emails changed or aliased at some point during the Okta-to-Entra migration, but I'd have to dig into that more.

Group and role-wise, I don't think that's it — we have affected users across different teams and roles. It doesn't seem to correlate with permissions or group membership.

---

### triage-agent

Thanks for digging into that — the plus-sign email pattern is a really strong clue. Here's my working theory:

When TaskFlow receives the authentication response from Entra ID, it tries to match the incoming identity to a local user account. If the email claim coming back from Entra ID doesn't exactly match what TaskFlow has stored (e.g., Entra sends `jane@company.com` but TaskFlow has `jane+taskflow@company.com`, or vice versa), TaskFlow can't find the user, so it kicks them back to login — causing the loop.

This would also explain why clearing cookies helps temporarily (a fresh session might trigger a slightly different flow) and why it's not all users (only those with plus-addressing or migrated/aliased emails).

Does this match what you're seeing? Specifically: can you check whether the email addresses stored in TaskFlow's user records for a few affected users exactly match what Entra ID is sending in the SAML/OIDC claims? If you can compare one affected user and one unaffected user, that would confirm this.

---

### reporter

That theory makes a lot of sense actually. I haven't looked at the SAML claims directly — I'm more on the IT admin side than a developer, so I'm not sure exactly how to pull that up from TaskFlow's end. But I can tell you that in Entra ID, the users with plus addressing have their primary email set to the regular version (like jane@company.com), whereas in TaskFlow their accounts were originally set up with the plus version (jane+taskflow@company.com) back when we were on Okta. So yeah, there's definitely a mismatch there for those users.

For the aliased users, I think something similar is going on — their email in Entra might not match what TaskFlow has on file because we consolidated some aliases during the migration.

One thing I'm not sure about though — if it's purely an email mismatch, why would incognito mode sometimes work? That part still seems odd to me. But the email explanation fits for most of the cases I've looked at.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop caused by email mismatch between Entra ID claims and TaskFlow user records after SSO migration","problem":"After migrating SSO from Okta to Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but are redirected back to the login page repeatedly. Affected users are those whose email address in Entra ID does not exactly match the email stored in TaskFlow's user database — primarily users with plus-sign addressing (e.g., TaskFlow has jane+taskflow@company.com but Entra sends jane@company.com) and users whose email aliases were consolidated during migration.","root_cause_hypothesis":"TaskFlow's SSO callback handler performs an exact string match between the email claim in the Entra ID authentication response and the email stored in the local user table. When there is no match, TaskFlow fails to find or create a session for the user and redirects them back to the login flow, creating an infinite loop. This was not an issue under Okta because Okta was likely configured to send the plus-addressed or aliased email that matched TaskFlow's records.","reproduction_steps":["Set up a user in TaskFlow with a plus-addressed email (e.g., testuser+taskflow@company.com)","Configure the same user in Entra ID with their canonical email (testuser@company.com) as the primary/claim email","Attempt to log into TaskFlow via SSO","Observe infinite redirect loop between TaskFlow and Entra ID"],"environment":"TaskFlow with Entra ID (Microsoft) SSO integration, migrated from Okta. SAML or OIDC-based authentication. Affects users with email mismatches between the IdP and TaskFlow's user store.","severity":"high","impact":"Approximately 30% of users are completely unable to log into TaskFlow. Workarounds (clearing cookies, incognito) are unreliable and temporary.","recommended_fix":"1. Investigate the SSO callback handler where the incoming email claim is matched against the local user table. Look for exact string comparison on the email field. 2. Implement email normalization before comparison — at minimum, strip plus-addressing suffixes (everything between + and @) and do a case-insensitive match. 3. Alternatively or additionally, match on a more stable identifier than email (e.g., Entra ID object ID / sub claim) and store it in the user record. 4. As an immediate remediation, update affected users' email addresses in TaskFlow's database to match what Entra ID sends, or configure Entra ID to send the plus-addressed email in the claim.","proposed_test_case":"Create users with emails containing plus-addressing (user+tag@domain.com) and aliases. Configure the IdP to return the canonical email without the plus suffix. Verify that SSO login succeeds and maps to the correct user account. Also verify that users with exactly matching emails continue to work.","information_gaps":["Whether TaskFlow uses SAML or OIDC for the Entra ID integration (does not change the fix approach, just which claim attribute to inspect)","The exact reason incognito/cookie clearing sometimes temporarily resolves the issue — likely cached redirect responses or stale session cookies from the failed auth attempt, but not confirmed","Whether there are affected users who do NOT fall into the plus-addressing or alias-mismatch categories"]}

---
