# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. One thing that would really help narrow this down: have you been able to identify any pattern among the ~30% of users who are affected? For example — are they in a specific team or role, were their accounts provisioned differently (e.g. SCIM vs manual), or do they tend to use a particular browser? Also, did the affected users have active/cached TaskFlow sessions from the Okta era when the switch happened, or were their sessions cleared as part of the migration?

---

### reporter

Good question. So we looked into this and the pattern we found is that the affected users tend to be the ones who use plus addressing in their email — like jane+taskflow@company.com — or users who had their email changed/aliased at some point during the Okta-to-Entra migration. The users who log in fine are the ones whose plain email address is the same in both systems. It doesn't seem to be browser-specific, we're seeing it on Chrome, Edge, and Firefox. As for sessions, we did clear all sessions as part of the migration, so it's not a caching issue from before the switch. One weird thing I noticed — sometimes if an affected user opens an incognito window, they can occasionally get in, but it's not consistent.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully with Entra ID but TaskFlow's auth callback fails to match them to an existing account, so it sends them back to login. The affected users are those with plus-addressed emails (e.g. jane+taskflow@company.com) or emails that were changed/aliased during the migration.","root_cause_hypothesis":"TaskFlow's SSO callback performs an exact string match between the email claim in the Entra ID token and the email stored in TaskFlow's user database. Okta likely preserved plus-addressed emails and aliases as-is in its tokens, but Entra ID either normalizes them (stripping the +tag), returns the primary alias instead of the one the user originally registered with, or uses a different claim field. When the lookup fails, TaskFlow treats the user as unauthenticated and redirects back to the IdP, creating the loop. The intermittent success in incognito windows may be due to Entra ID occasionally returning a different claim format depending on session state or prompt behavior.","reproduction_steps":["Set up a TaskFlow instance with Entra ID SSO configured","Create a user account in TaskFlow with a plus-addressed email (e.g. testuser+taskflow@company.com)","Ensure the corresponding Entra ID account has the base email (testuser@company.com) as primary, or vice versa","Attempt to log in via SSO","Observe the redirect loop: Entra ID authenticates successfully, but TaskFlow redirects back to Entra ID immediately"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects Chrome, Edge, and Firefox equally. Sessions were cleared during migration. Not a caching or stale-session issue.","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely. No reliable workaround exists (incognito works inconsistently). These users can access other SSO-protected apps, so the problem is TaskFlow-specific.","recommended_fix":"1. Inspect the SAML assertion or OIDC token that Entra ID sends back — log the exact email claim value for an affected user and compare it to what TaskFlow has stored in its user table. 2. Check which claim field TaskFlow reads (e.g. `email`, `preferred_username`, `upn`) and whether Entra ID populates it differently than Okta did. 3. Fix the user lookup to normalize emails before comparison: strip plus-address tags, do case-insensitive matching, and/or check against known aliases. 4. Consider adding a claim mapping configuration so admins can specify which Entra ID claim maps to the TaskFlow user identifier. 5. For immediate relief, affected users' emails in TaskFlow's database could be updated to match what Entra ID returns.","proposed_test_case":"Unit test for the SSO callback user-lookup function: given a stored email of 'jane+taskflow@company.com' and an incoming token with email claim 'jane@company.com' (and vice versa), the lookup should successfully match the user. Include cases for case-insensitive matching and alias resolution.","information_gaps":["Exact claim field and value Entra ID returns for an affected user (not yet inspected at the token level)","Whether TaskFlow uses SAML or OIDC for the SSO integration","Whether a user-alias or email-mapping table exists in TaskFlow's data model"],"clarity_scores":{"symptom":0.9,"cause":0.75,"reproduction":0.8,"impact":0.85,"overall_ambiguity":0.17}}

---

