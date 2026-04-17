# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that's a tricky one.

The fact that only ~30% of users are affected is the most important clue here. Have you been able to identify any pattern among the affected users? Specifically:

- Were they previously logged into TaskFlow with active sessions when the SSO switch happened (i.e., could this be stale session cookies/tokens from the Okta integration)?
- Do the affected users belong to a different group, role, or organizational unit in Entra ID compared to the ones who can log in fine?
- Does clearing browser cookies and all site data for your TaskFlow domain resolve the loop for affected users?

If you haven't tested the cookie-clearing approach yet, that would be the quickest way to narrow this down.

---

### reporter

Good questions. We did try having affected users clear their cookies and site data — it sometimes works temporarily. Like, they'll clear everything, try again, and occasionally get in, but it's not consistent. Some users have had better luck using incognito/private browsing windows, which is weird.

As for a pattern — I've been looking into that. It's not really about groups or roles in Entra ID, since we have affected and unaffected users in the same departments and roles. What I did notice is that a lot of the affected users have '+' characters in their email addresses (we use plus addressing for some folks, like jane+taskflow@company.com). Also, a few users who are affected had their email addresses changed or aliased at some point — they originally signed up for TaskFlow with one email and later we set up an alias in Entra ID.

The users who work fine tend to be the ones with straightforward email addresses that haven't changed. I hadn't connected those dots until you asked me to look for a pattern, but that seems to be the common thread.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with '+' in email or aliased emails after Okta-to-Entra ID SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Users authenticate successfully with Entra ID but are immediately redirected back to the identity provider upon returning to TaskFlow. The affected population correlates strongly with users who have '+' characters in their email addresses (plus addressing) or whose email addresses were changed/aliased after initial TaskFlow signup.","root_cause_hypothesis":"TaskFlow's authentication callback is failing to match the identity returned by Entra ID to existing user records, likely due to one or both of these issues: (1) The '+' character in email addresses is being URL-encoded or decoded incorrectly during the OAuth2/OIDC callback ('+' becomes a space in application/x-www-form-urlencoded contexts), causing the email claim to not match the stored user record. (2) For aliased users, the email claim Entra ID returns differs from what Okta previously returned (e.g., Entra sends the primary alias while TaskFlow stored the original signup email). When the match fails, TaskFlow likely does not establish a valid session, so the auth middleware redirects back to the IdP, creating the loop. The inconsistent success with cookie-clearing and incognito windows suggests that occasionally the token round-trip preserves the email correctly (perhaps via a different encoding path or cached token), but the default flow corrupts it.","reproduction_steps":["Configure TaskFlow SSO with Microsoft Entra ID","Create a user account with a plus-addressed email (e.g., jane+taskflow@company.com)","Attempt to log in via SSO","Observe: successful Entra ID authentication followed by redirect back to TaskFlow, which immediately redirects back to Entra ID in a loop","Compare: repeat with a user whose email has no special characters — login succeeds"],"environment":"TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Users affected have '+' in email addresses or email aliases/changes. Other Entra ID-integrated apps work fine for these same users.","severity":"high","impact":"Approximately 30% of the team is effectively locked out of TaskFlow. Workaround (incognito/cookie clearing) is unreliable. Productivity impact is significant as affected users cannot consistently access the task management application.","recommended_fix":"Investigate TaskFlow's OAuth2/OIDC callback handler, specifically: (1) How the email claim from the ID token or userinfo endpoint is parsed — check for incorrect URL decoding that converts '+' to space. (2) How the parsed email is matched against stored user records — the match query should normalize or be case-insensitive and encoding-aware. (3) Whether TaskFlow stores the original signup email from Okta and compares it against what Entra ID returns — if Entra sends a different alias, the lookup fails. Fix should include: properly handling RFC 5322 email characters in the auth callback, and matching users by a stable identifier (like the OIDC 'sub' claim or an internal user ID) rather than relying solely on email string comparison.","proposed_test_case":"Create integration tests for the SSO callback that (1) send an ID token with a plus-addressed email and verify the user session is created correctly, (2) send an ID token where the email claim differs from the stored user email (alias scenario) and verify matching falls back to a secondary identifier, and (3) verify no redirect loop occurs when the email contains URL-sensitive characters (+, %, etc.).","information_gaps":["Whether TaskFlow uses OIDC or SAML with Entra ID (affects where the email encoding issue occurs)","The exact claim TaskFlow uses for user matching (email vs. sub vs. preferred_username)","Server-side logs from TaskFlow during a failed login attempt — would confirm the matching failure","Whether the Okta integration used a different claim or email format that masked this bug"],"confidence":{"root_cause":"medium","reproduction":"high","severity_assessment":"high"}}

---
