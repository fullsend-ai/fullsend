# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up. The fact that only ~30% of users are affected is a strong clue — I'd like to understand what sets them apart.

Have you noticed any pattern among the affected users? For example:

1. **Stale browser state** — They hadn't cleared cookies/sessions since the Okta→Entra switch (and clearing cookies fixes it)
2. **Group or role difference** — They belong to specific teams, roles, or Entra ID groups that the unaffected users don't
3. **Account attribute** — Something about their accounts is different (e.g., created earlier, different email domain, federated vs cloud-only identity)
4. **Browser/device** — They're using a specific browser or device type
5. **Something else or not sure yet**

Also — if any affected user has tried logging in from a private/incognito window, did that change the behavior?

---

### reporter

Good question. I've been looking into this and I think it's account-related. The users who are affected seem to fall into two camps: some of them use plus-addressed emails (like jane+taskflow@company.com), and others had their email changed or aliased at some point — like they originally signed up with one email and then we added an alias in Entra ID. The users who work fine are the ones whose straightforward primary email is the same as what they had in Okta.

It's not a browser thing — we've seen it on Chrome, Edge, and Firefox.

And yes, actually, a couple of affected users tried incognito/private windows and it sometimes works there, weirdly enough. Also clearing all cookies and trying again quickly sometimes lets them in, but it's not reliable.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users whose Entra ID email claim doesn't match stored TaskFlow email (post-SSO migration)","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Entra ID but TaskFlow cannot establish a session, sending them back to the IdP repeatedly. Affected users are those with plus-addressed emails (e.g. jane+taskflow@company.com) or users whose email was changed/aliased after initial signup. Unaffected users have a straightforward primary email that is identical to what was in Okta.","root_cause_hypothesis":"TaskFlow's SSO callback matches the incoming identity token's email claim against the user table using an exact string match. Entra ID returns a different email value than Okta did for these users — likely the primary/canonical email rather than the plus-addressed or aliased variant that Okta was configured to send. When the match fails, TaskFlow cannot locate the user record, fails to create a valid session, and issues a new login redirect. Because the user is already authenticated at the IdP, Entra ID immediately redirects back, creating the loop. The intermittent success with incognito/cookie-clearing suggests the loop is self-reinforcing via cached session or OIDC state cookies.","reproduction_steps":["1. Have a user account in TaskFlow whose stored email is a plus-address (e.g. jane+taskflow@company.com) or differs from their Entra ID primary email.","2. Attempt to log in via SSO.","3. Authenticate successfully at Microsoft Entra ID.","4. Observe the redirect loop: TaskFlow → Entra ID → TaskFlow → Entra ID (repeating).","5. Inspect the email claim in the OIDC/SAML token returned by Entra ID and compare it to the email stored in TaskFlow's user table for that user."],"environment":"Any browser (Chrome, Edge, Firefox). Server-side issue tied to SSO callback handling and user-matching logic. Entra ID as the IdP with standard OIDC/SAML redirect flow.","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely. These are users with plus-addressed emails or aliased accounts — likely long-tenured or power users. No reliable workaround exists (incognito/cookie-clearing is inconsistent).","recommended_fix":"1. **Immediate**: Inspect the SSO callback handler to confirm it matches users by exact email string comparison. Log the email claim from incoming Entra ID tokens for affected users and compare against stored emails. 2. **Short-term fix**: Normalize email comparison (case-insensitive, strip plus-addressing) OR match on a stable claim like `sub` (OIDC subject) or `oid` (Entra object ID) instead of email. 3. **Data migration**: Build a one-time mapping script that links existing TaskFlow user records to their Entra ID object IDs, so future matches are identity-based rather than email-based. 4. **Session handling**: Ensure the SSO callback returns a clear error (e.g. 'account not found') instead of silently redirecting back to login when no user match is found, to prevent loops.","proposed_test_case":"Create test users with (a) a plus-addressed email, (b) an email that differs from the IdP-returned primary email, and (c) a straightforward matching email. Mock the OIDC callback with tokens containing each email variant. Assert that all three users are correctly matched to their TaskFlow accounts and receive valid sessions. Additionally, assert that a token with a completely unknown email returns an explicit error response (HTTP 403 or error page) rather than a redirect to the login endpoint.","information_gaps":["Whether TaskFlow uses OIDC or SAML for the Entra ID integration (affects which claim carries the email)","The exact claim being used for user matching (email, preferred_username, UPN, or nameID)","Whether Entra ID's optional claims or token configuration was customized to match the old Okta claim set","Whether any affected user has checked browser DevTools network tab to confirm the specific redirect response codes (302 vs 303) and any error parameters in the redirect URL"]}

---
