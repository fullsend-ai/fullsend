# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the redirect loop behavior is clear. Could you share the environment details for the affected users? Specifically:

- What browser(s) are they using (and versions)?
- What OS are they on?
- What version of TaskFlow are you running?
- Is there any difference in environment between the ~30% who are affected and those who aren't (e.g., different browsers, different Entra ID group memberships, accounts migrated differently from Okta)?

---

### reporter

The affected users are on a mix of browsers — Chrome, Edge, and Firefox. We're running TaskFlow v2.3.1, self-hosted. As for the difference between affected and unaffected users, I did notice a pattern: it seems like the users who are stuck in the loop tend to be ones who use plus addressing in their email (like jane+taskflow@company.com) or users whose email changed at some point — like they had one email in Okta and then we set up an alias in Entra ID. The users who can log in fine are the ones whose plain email address is the same in both systems. Oh, and one weird thing — a couple of affected users said they could sometimes get in using an incognito window, but it's not consistent.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with plus-addressed or migrated email addresses after Okta-to-Entra ID migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. Users authenticate successfully with Microsoft but are immediately redirected back to the IdP upon returning to TaskFlow. The affected population correlates with users who use plus addressing (e.g., jane+taskflow@company.com) or whose email address changed between the Okta and Entra ID configurations.","root_cause_hypothesis":"TaskFlow is likely matching the authenticated user by comparing the email claim from the OIDC/SAML token against stored user records using an exact string match. When Entra ID returns 'jane@company.com' but the TaskFlow account was created under 'jane+taskflow@company.com' (or vice versa), or when the stored email reflects the old Okta identity, the lookup fails. Without a matching session/user record, TaskFlow treats the user as unauthenticated and initiates a new login flow, causing the loop. The intermittent success in incognito windows may be due to cached cookies or stale session tokens from the old Okta integration interfering in normal browser sessions.","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO","Create or have a user account whose email in TaskFlow uses plus addressing (e.g., user+taskflow@company.com) or differs from their Entra ID primary email","Attempt to log in via SSO","Observe that authentication succeeds at Microsoft but TaskFlow redirects back to Microsoft in a loop"],"environment":"TaskFlow v2.3.1, self-hosted. Multiple browsers affected (Chrome, Edge, Firefox). SSO provider: Microsoft Entra ID (migrated from Okta).","severity":"high","impact":"~30% of the organization is locked out of TaskFlow entirely. These users cannot work in the application. Workarounds (incognito windows) are unreliable.","recommended_fix":"Investigate the user lookup logic in the SSO callback handler. Check how TaskFlow resolves the identity claim (email/sub/UPN) from the Entra ID token to a local user record. Likely fixes: (1) Normalize email addresses before comparison — strip plus-address tags and compare case-insensitively. (2) Add a migration step or admin tool to update stored user emails to match Entra ID primary identities. (3) Consider matching on a stable claim like the OIDC 'sub' rather than email. Also investigate whether stale Okta session cookies are contributing to the loop and add logic to clear legacy SSO cookies on the new login flow.","proposed_test_case":"Create test users with plus-addressed emails and with emails that differ from their IdP primary email. Verify that SSO login completes successfully and resolves to the correct TaskFlow account without looping. Also test that a user with a stale session cookie from a previous IdP configuration can log in cleanly.","information_gaps":["No server-side logs or browser network traces to confirm the exact point of failure in the redirect loop","Unknown whether TaskFlow matches on email, UPN, or OIDC sub claim","Unknown whether clearing browser cookies/cache reliably resolves the issue for affected users"]}

---

