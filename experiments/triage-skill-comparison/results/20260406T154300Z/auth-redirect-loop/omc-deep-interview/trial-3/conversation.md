# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. To help narrow this down: have you noticed any pattern that distinguishes the ~30% of affected users from those who can log in fine? For example — are the affected users on a different domain or email format, were their accounts provisioned differently (e.g., pre-existing local accounts vs. JIT-provisioned), or do they perhaps have any special characters or legacy attributes in their Okta profiles that carried over? Even a hunch would help us investigate.

---

### reporter

Good question — now that you mention it, I think there might be a pattern. A lot of the affected users have '+' in their email addresses. We use plus addressing for some folks, like jane+taskflow@company.com. Also, a few of the affected users had their emails changed at some point — they originally signed up for TaskFlow with one email and then we aliased them to a different primary email when we set up Entra ID. The users who can log in fine seem to be the ones with straightforward email addresses that haven't changed. I hadn't connected those dots until you asked, but that does seem to line up.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users with '+' in email or changed email addresses after Okta-to-Entra migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, but TaskFlow immediately redirects them back to Microsoft again. The session is never established. Affected users can log into other Entra-ID-backed apps without issue.","root_cause_hypothesis":"TaskFlow's user-matching logic compares the email claim from the Entra ID token against its stored user records to establish a session. This match is failing for two groups: (1) users with '+' in their email (e.g., jane+taskflow@company.com) — the '+' may be URL-encoded to '%2B' by Entra ID or during the redirect, causing a mismatch; (2) users whose primary email was changed/aliased during the migration — the email in the Entra ID token doesn't match the original email stored in TaskFlow's user table. When the match fails, TaskFlow treats the user as unauthenticated and restarts the login flow, creating the loop.","reproduction_steps":["Set up TaskFlow with Entra ID SSO integration","Create or use a test user account with a '+' character in the email address (e.g., testuser+taskflow@company.com)","Attempt to log into TaskFlow via SSO","Observe: authentication succeeds at Microsoft but TaskFlow redirects back to Microsoft in an infinite loop","Alternatively: create a user with one email, then change their primary email in Entra ID to a different address, and attempt login"],"environment":"TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Entra ID app registration and redirect URIs verified as correct. Affects ~30% of users — those with '+' in emails or changed/aliased email addresses.","severity":"high","impact":"~30% of users are completely locked out of TaskFlow with no workaround. This is a blocker for those users since the SSO migration has already been completed.","recommended_fix":"Investigate TaskFlow's SSO callback handler — specifically how it extracts the user identity from the Entra ID token and matches it to local user records. (1) Ensure the email comparison handles URL-encoding (decode '%2B' back to '+' before matching). (2) Ensure the comparison is case-insensitive. (3) For users with changed emails, consider matching on a stable identifier (such as the OIDC 'sub' claim or Entra Object ID) rather than email alone, or add support for matching against email aliases/previous emails. As a quick mitigation, a lookup against both the current and previous email fields (if stored) would unblock aliased users.","proposed_test_case":"Create integration tests for the SSO callback handler that verify: (a) a user with '+' in their email (e.g., 'user+tag@domain.com') is correctly matched and logged in; (b) a user whose email was changed from 'old@domain.com' to 'new@domain.com' can log in when the token contains 'new@domain.com'; (c) URL-encoded email values in the callback are properly decoded before user lookup; (d) no redirect loop occurs when the user match succeeds.","information_gaps":["Exact SSO protocol in use (SAML 2.0 vs. OIDC) — affects where the email claim is extracted from","Which claim/attribute TaskFlow uses for user matching (email, nameID, sub, UPN)","Whether TaskFlow stores the original Okta subject identifiers and whether those could be leveraged","Server-side logs from the redirect loop showing the exact point of failure"],"clarity_scores":{"symptom":0.9,"cause":0.75,"reproduction":0.7,"impact":0.7,"overall_ambiguity":0.215}}

---

