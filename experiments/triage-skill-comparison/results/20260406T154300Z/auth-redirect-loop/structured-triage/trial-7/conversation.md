# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the redirect loop behavior and the 30% affected rate are really helpful context.

Could you share the environment details for a few of the affected users? Specifically:
- What browser(s) and version(s) are the affected users on?
- What OS are they using?
- What version of TaskFlow are you running?

Since only some users are affected, it would also be useful to know if you've noticed any pattern that distinguishes them from the users who can log in fine (e.g., different browser, different OS, different role or group membership in Entra ID).

---

### reporter

We're running TaskFlow v2.3.1, self-hosted. The affected users are on a mix of browsers — Chrome, Edge, and Firefox — so it doesn't seem to be browser-specific. Most are on Windows 10/11, a couple on macOS. Same mix as the users who can log in fine, so I don't think it's a browser or OS thing.

As for a pattern — I've been trying to figure that out. The ones who are affected seem to be users who either have a '+' in their email address (we use plus addressing for some people, like jane+taskflow@company.com) or users who originally signed up for TaskFlow with a different email that we later set up as an alias in Entra ID. The users who work fine are the ones whose straightforward primary email matches what they've always used. I'm not 100% sure that's the exact pattern but it's what I've noticed so far.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with plus-addressed emails or Entra ID aliases after Okta-to-Entra migration","problem":"After migrating from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Microsoft but are immediately redirected back to the identity provider upon returning to TaskFlow. Affected users can log into other SSO-integrated apps without issue.","root_cause_hypothesis":"TaskFlow is likely matching the email claim from the Entra ID token against its stored user records using an exact string comparison. Users with '+' in their email (e.g., jane+taskflow@company.com) or users whose Entra ID primary email differs from the email originally used to create their TaskFlow account fail this match. When TaskFlow cannot associate the authenticated identity with a local user, it treats the session as unauthenticated and re-initiates the SSO flow, causing the loop.","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO","Create a user in TaskFlow with a plus-addressed email (e.g., jane+taskflow@company.com) or with an email that differs from their Entra ID primary email","Attempt to log in as that user via SSO","Observe the infinite redirect loop: TaskFlow → Microsoft login → successful auth → TaskFlow → Microsoft login → ..."],"environment":"TaskFlow v2.3.1, self-hosted. Mixed client environments (Chrome/Edge/Firefox on Windows 10/11 and macOS). Recently migrated SSO from Okta to Microsoft Entra ID.","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. This is a total access blocker for affected users with no known workaround.","recommended_fix":"Investigate the SSO callback handler where TaskFlow matches the identity token's email claim to a local user record. Check whether: (1) plus-addressed emails are being normalized or matched correctly, (2) the email claim from Entra ID tokens matches what TaskFlow has stored (Okta may have sent a different claim format than Entra ID), (3) alias emails are checked in addition to primary emails. Consider normalizing email comparison (case-insensitive, plus-address stripping) or matching on a more stable identifier like the OIDC 'sub' claim rather than email alone.","proposed_test_case":"Write an integration test for the SSO callback that authenticates with a token containing a plus-addressed email (user+tag@domain.com) and verifies the user is matched to the correct account and logged in successfully, rather than redirected back to the IdP.","information_gaps":["Exact email claim(s) Entra ID is returning in the token vs. what TaskFlow has stored for affected users","Server-side logs from TaskFlow during a failed login attempt (would confirm whether the match failure is the cause)","Whether Okta was configured to send a different email claim format than Entra ID defaults to"]}

---
