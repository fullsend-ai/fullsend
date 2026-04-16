# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. Before we dig into the technical side, I want to understand the pattern between affected and unaffected users.

Do the ~30% of affected users have anything in common that distinguishes them from the rest? For example:

- **A)** They had existing active sessions/cookies from the old Okta SSO when the switch happened
- **B)** They're on a different domain, group, or OU in your directory (e.g., contractors vs. employees)
- **C)** They're using a different browser or device type than the unaffected users
- **D)** Something else, or you haven't been able to identify a pattern yet

This will help narrow down whether this is a session/cookie issue, a claims-mapping issue, or something else entirely.

---

### reporter

Good question. I've been trying to figure out the pattern myself. It's not a browser thing — I've seen it happen on both Chrome and Edge for the same user. And it's not a department or group thing either.

What I did notice is that several of the affected users have those plus-sign email addresses, like jane+taskflow@company.com. We use plus addressing for some users to route things. I'm not 100% sure that's the only common factor though — there might be a couple affected users who don't have the plus sign but I think those ones had their email changed or aliased at some point during the Okta days.

One weird thing — a couple users said they were able to get in using an incognito window, but it doesn't always work. Not sure if that's relevant.

---

### triage-agent

Thanks — the plus-sign email pattern is a really strong clue. Here's what I think is happening:

When a user authenticates, TaskFlow likely matches the email claim from the SSO token against its stored user records. Okta and Entra ID may format plus-addressed emails differently in their tokens — for example, Okta might have sent `jane+taskflow@company.com` while Entra ID sends `jane@company.com` (stripping the plus tag), or vice versa. When TaskFlow can't match the incoming email to a user, it fails the session and redirects back to login, creating the loop. The incognito window occasionally working could be because it avoids a stale session cookie from a previous failed attempt.

Does that match what you're seeing? Specifically: can you check the TaskFlow user records for an affected user and confirm what email address is stored there — is it the plus-addressed form (e.g., `jane+taskflow@company.com`) or the base address (`jane@company.com`)? That would confirm whether this is a claims-mismatch issue.

---

### reporter

Yeah, that theory makes sense to me. I just checked and the affected users I looked at have the plus-addressed form stored in TaskFlow — like jane+taskflow@company.com. So if Entra is sending back just jane@company.com in the token, that would explain why it can't find a match.

For the couple users who don't have plus addresses but are still affected, I think those are the ones whose emails were changed at some point — like they originally signed up with a personal email and then we aliased them to a company email in Okta. So their TaskFlow account might be under the old email while Entra is sending the new one.

I'm still a little confused about the incognito thing though — if it's purely an email mismatch, why would incognito sometimes help?

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop: email claim mismatch between Entra ID tokens and TaskFlow user records","problem":"After migrating SSO from Okta to Microsoft Entra ID, ~30% of users experience an infinite redirect loop during login. They authenticate successfully with Entra ID but TaskFlow fails to match them to a user record, invalidates the session, and redirects back to the IdP. The affected users fall into two categories: those with plus-addressed emails (jane+taskflow@company.com) and those whose email was changed or aliased during the Okta era.","root_cause_hypothesis":"TaskFlow matches the incoming SSO token's email claim against its stored user records. Entra ID normalizes or strips plus-addressed email tags (sending `jane@company.com` instead of `jane+taskflow@company.com`), or sends a different claim attribute (e.g., UPN vs. email) than Okta did. For aliased users, Entra ID sends their current canonical email while TaskFlow still stores the old one. In both cases, the lookup fails, TaskFlow cannot establish a session, and the user is redirected back to the IdP in a loop.","reproduction_steps":["Identify a user whose TaskFlow account email is a plus-addressed form (e.g., jane+taskflow@company.com)","Have that user attempt to log into TaskFlow via Microsoft Entra ID SSO","Observe successful Entra ID authentication followed by redirect back to TaskFlow","TaskFlow fails to match the email claim to a user record and redirects back to Entra ID","The cycle repeats indefinitely"],"environment":"TaskFlow instance with SSO recently migrated from Okta to Microsoft Entra ID. Affected users have plus-addressed emails or historically changed/aliased emails in TaskFlow's user store. Issue is browser-independent (reproduced on Chrome and Edge).","severity":"high","impact":"~30% of the team cannot log into TaskFlow at all. Complete loss of access for affected users with no reliable workaround (incognito works intermittently).","recommended_fix":"1. Inspect the Entra ID token claims for an affected user (decode the id_token or check server logs) to confirm which email value Entra ID is actually sending and which claim attribute it uses (email, preferred_username, UPN). 2. Compare that against TaskFlow's user lookup logic. 3. Fix the lookup to normalize email comparison — either strip plus tags before matching, or match on a canonical identifier. 4. For aliased users, add a migration step or secondary-email lookup to match old stored emails to current Entra ID identities. 5. Consider adding a user-mapping table or matching on a stable IdP subject identifier (sub/oid) rather than email, which is inherently mutable.","proposed_test_case":"Create test users with plus-addressed emails (user+tag@domain.com) and with an email that differs from the IdP claim. Verify that SSO login successfully matches these users to their TaskFlow accounts and establishes a session without redirect loops.","information_gaps":["Exact claim attribute and value Entra ID sends in the token (developer can decode a token or check auth logs to confirm)","Whether TaskFlow's auth code has explicit email normalization logic or relies on exact string matching (code inspection needed)","Why incognito occasionally works — likely a stale session cookie from a prior failed auth attempt causes the loop to persist even after a correct match; incognito avoids this. Developer should verify session cleanup on auth failure."]}

---
