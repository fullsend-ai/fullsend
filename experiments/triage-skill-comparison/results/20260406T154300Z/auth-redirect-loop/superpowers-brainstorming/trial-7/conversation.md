# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up. The fact that only ~30% of users are affected is a really useful clue — let's dig into that.

Have you noticed any pattern that distinguishes the affected users from those who can log in fine? For example:

1. **They had active sessions before the switch** (i.e., they were logged into TaskFlow with Okta and didn't clear cookies/sessions)
2. **They're in a different group or role** in Entra ID (e.g., different security group, different MFA policy)
3. **They're using a different browser or device** than the unaffected users
4. **Something else you've noticed**

Even a hunch here would help narrow this down quickly.

---

### reporter

Good question. It's not a browser or device thing — we've seen it on Chrome, Edge, and Firefox. And it's not related to groups or roles either, we checked that. The pattern we've noticed is that the affected users tend to be the ones who use plus-addressed emails (like jane+taskflow@company.com) or users whose email addresses were set up differently in Entra than they were in Okta — like some people had their emails aliased when we migrated. The users who log in fine are the ones whose email addresses are straightforward and match exactly between the old Okta setup and Entra. We didn't think much of it at first but that does seem to be the common thread.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users whose email claim from Entra ID doesn't match stored TaskFlow email","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication with Entra succeeds, but upon redirect back to TaskFlow, the user is immediately sent back to the IdP. The affected users are those with plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses were aliased/mapped differently between Okta and Entra ID.","root_cause_hypothesis":"TaskFlow's OIDC/SAML callback handler matches the incoming identity token's email claim against stored user records using exact string comparison. When the email claim returned by Entra ID differs from what Okta previously returned (due to plus-addressing, alias normalization, or different primary-email selection), the user lookup fails. Rather than returning an authentication error, the app treats the user as unauthenticated and initiates a new SSO flow, creating the redirect loop.","reproduction_steps":["Set up a user in TaskFlow whose stored email was provisioned via Okta with a plus-address or alias (e.g., jane+taskflow@company.com)","Configure Entra ID as the SSO provider, where the same user's primary email or preferred_username claim resolves to a different value (e.g., jane@company.com)","Attempt to log in as that user","Observe the redirect loop: Entra authenticates successfully, TaskFlow callback fails to match the user, redirects back to Entra"],"environment":"TaskFlow with Microsoft Entra ID as SSO provider (recently migrated from Okta). Occurs across all browsers (Chrome, Edge, Firefox). Not related to Entra ID groups, roles, or MFA policies.","severity":"high","impact":"~30% of the team cannot log into TaskFlow at all. These users are completely blocked from using the application. Workarounds likely require manual database changes or email reconfiguration in the IdP.","recommended_fix":"1. Inspect the SSO callback handler's user-lookup logic — find where the email claim from the token is matched against stored user records. 2. Determine which claim Entra ID is returning (email, preferred_username, upn) vs. what Okta returned. 3. Fix the mismatch by either: (a) normalizing emails before comparison (strip plus-addressing, case-insensitive match), (b) adding a lookup fallback that matches on a stable identifier like the OIDC 'sub' claim rather than email alone, or (c) adding an email-alias mapping table. 4. As an immediate mitigation, update the affected users' stored emails in TaskFlow to match what Entra ID returns, or configure Entra ID custom claims to emit the plus-addressed form.","proposed_test_case":"Create test users with plus-addressed emails and email aliases that differ between the IdP token claim and the stored TaskFlow user record. Verify that SSO login succeeds for these users without redirect loops, and that the correct TaskFlow user account is resolved regardless of email format variations.","information_gaps":["Which specific OIDC/SAML claim TaskFlow uses for user lookup (email, preferred_username, upn, sub)","Whether TaskFlow uses OIDC or SAML for SSO — affects which claims are available","Whether user records are matched solely by email or also by an immutable subject identifier"]}

---

