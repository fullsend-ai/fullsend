# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the redirect loop behavior and the partial-impact pattern are really helpful.

Could you share the environment details for the affected users? Specifically:
- What browser(s) are they using (and versions)?
- What OS are they on?
- What version of TaskFlow are you running?
- Any relevant configuration differences (e.g., are affected users on a VPN, a different network, or using a different authentication method prior to the switch)?

---

### reporter

The affected users are on a mix of browsers — Chrome, Edge, and Firefox. We've seen it on both Windows and Mac. We're running TaskFlow v2.3.1, self-hosted. No VPN differences or network differences that I can see — affected and unaffected users are on the same network. The one pattern I've noticed is that the affected users tend to be the ones who use plus-addressed emails (like jane+taskflow@company.com) or users whose email addresses were set up as aliases when we migrated to Entra ID. The users who log in fine seem to be the ones whose primary email in Entra matches exactly what they had in Okta.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop after Okta-to-Entra-ID migration for users with plus-addressed or aliased emails","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Microsoft but are immediately redirected back to the identity provider upon returning to TaskFlow. The issue correlates with users who have plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses were set up as aliases during the Entra ID migration, rather than being primary emails.","root_cause_hypothesis":"TaskFlow's session or identity matching logic likely compares the email claim returned by the IdP against stored user records using an exact string match. Entra ID is probably returning the user's primary email address in the token claims, which does not match the plus-addressed or aliased email stored in TaskFlow's user table. The failed match prevents session creation, so TaskFlow treats the user as unauthenticated and redirects back to the IdP, creating the loop. Under Okta, the email claim may have been configured to return the exact plus-addressed or alias form.","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID as the SSO provider","Create or identify a user whose TaskFlow account email is a plus-addressed email (e.g., jane+taskflow@company.com) or an Entra ID alias rather than their primary Entra ID email","Attempt to log in as that user via SSO","Observe that after successful Microsoft authentication, the user is redirected back to Microsoft in a loop instead of landing in TaskFlow"],"environment":"TaskFlow v2.3.1 (self-hosted), Microsoft Entra ID SSO, multiple browsers (Chrome, Edge, Firefox), Windows and macOS, no VPN or network variance","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. This blocks all productivity for affected users with no known workaround.","recommended_fix":"Investigate how TaskFlow matches the identity token's email claim to local user records. Check whether Entra ID returns the primary email rather than the plus-addressed or alias form in the `email` or `preferred_username` claim. Likely fixes: (1) configure the Entra ID app registration to emit the correct email claim (e.g., via optional claims or claims mapping policy), or (2) update TaskFlow's user-matching logic to normalize email addresses (strip plus-addressing suffixes, check against known aliases) before matching, or (3) update affected user records in TaskFlow to use their Entra ID primary email.","proposed_test_case":"Create a test user in Entra ID whose primary email is user@company.com but whose TaskFlow account uses user+taskflow@company.com. Initiate SSO login and verify that the user is authenticated and a session is created without a redirect loop. Repeat with an alias email scenario.","information_gaps":["No server-side logs or browser network traces showing the specific token claims or session-creation failure","Exact SAML/OIDC claim configuration in the Entra ID app registration (which claim field carries the email)","Whether TaskFlow uses SAML or OIDC for SSO integration"]}

---

