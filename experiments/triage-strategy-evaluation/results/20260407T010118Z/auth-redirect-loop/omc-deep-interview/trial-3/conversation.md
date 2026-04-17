# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that redirect loop sounds frustrating. One thing that would really help narrow this down: have you noticed any pattern among the 30% of users who are affected? For example, do they share anything in common like being in a specific Entra ID group, having accounts that were provisioned differently (e.g. federated vs cloud-only), or having existing active sessions/cookies from the old Okta setup when the switch happened?

---

### reporter

Good question. I haven't looked at Entra ID groups specifically, but now that you mention it, I did notice something — a lot of the affected users have those 'plus' email addresses, like jane+taskflow@company.com. We use plus addressing for some people to route things. Not sure if that's all of them though. Also, I think a few of the affected users had their email addresses changed at some point — like they were onboarded with a personal email and later we aliased their company email in the directory. The users who work fine seem to just have straightforward email addresses. Oh and we did tell everyone to clear cookies before the switch but I'm not sure everyone did that.

---

### triage-agent

That plus-addressing pattern is a really useful clue — it could definitely explain the selective failures. To dig into this further, could you check TaskFlow's server-side logs for one of the affected users during a failed login attempt? Specifically, I'm looking for what happens right after Microsoft redirects back to TaskFlow — does TaskFlow log the incoming identity token or the email/UPN claim it receives? And does it log any kind of user-lookup failure or session-creation error before it kicks off the redirect again? Even a snippet of the relevant log lines would help pinpoint exactly where the loop starts.

---

### reporter

Honestly I'm not super familiar with the server-side logs — I'm more on the IT admin side than a developer. I can try to get someone to look at them, but I did poke around in the browser dev tools for one of the affected users and I noticed something kind of weird. When they get redirected back from Microsoft to TaskFlow, there's a Set-Cookie header in the response, but then on the very next request back to TaskFlow the cookie doesn't seem to be there. It's like the cookie isn't sticking. I'm not sure if that helps or if I'm reading the dev tools wrong. I haven't been able to check the actual server logs yet — is there a specific log file or setting I should look at in TaskFlow?

---

### triage-agent

That cookie observation is really helpful — you're reading the dev tools right, and that's a significant clue. Here's something that could confirm the root cause quickly: would you be able to pick one of the affected users who has a plus-address (like jane+taskflow@company.com) and temporarily give them a plain alias (like jane@company.com) in Entra ID, then have them try logging in with that? If the loop stops, we'll know the plus sign in the email is what's tripping things up.

Also, could you do a quick cross-check — of the affected users, do *all* of them have either a plus-address or a changed/aliased email? And conversely, does anyone with a plain, never-changed email experience the loop? That would tell us whether we're looking at one problem or two.

---

### reporter

I can try setting up a plain alias for one of the plus-address users — that's a good idea. Give me a day or so to coordinate that with them.

As for the cross-check, I went through the list just now and I think it mostly holds up. The ones I can clearly identify are either plus-address users or people whose emails were changed during onboarding. I don't think anyone with a normal, unchanged email is having the issue, but I'm not 100% certain on a couple of them — I'd have to dig into their account history to be sure. It does seem like it's one of those two things though for the majority of affected users.

---

### triage-agent

That cross-check is really helpful — the pattern is looking solid. While you're waiting to coordinate the alias test, could you do one more quick thing in browser dev tools for an affected plus-address user? When they hit the redirect loop, look at the Network tab and find the response from TaskFlow right after the Microsoft redirect (it'll be the one with the `Set-Cookie` header you noticed). Could you share the exact `Set-Cookie` header value — specifically the cookie name and its attributes like `Domain`, `Path`, and `SameSite`? You can redact any tokens or personal info, I'm mainly interested in whether the cookie value contains the user's email address and whether the `+` character appears in it (or if it's been replaced with a space or something else). That would help us confirm exactly where the plus sign is causing the breakage.

---

### reporter

OK I pulled up the dev tools again for one of the plus-address users. The cookie name is something like `taskflow_session` (or maybe `tf_session`, I'm going from memory on the exact name). I can see it has `SameSite=Strict` set on it, and `Path=/`. I don't think I see a `Domain` attribute explicitly listed — maybe that means it defaults to the TaskFlow domain? As for the cookie value itself, it looks like an encrypted or encoded blob — like a long random-looking string, not a readable email address. So I can't tell if the plus sign is in there or not. The main thing I notice is that the Set-Cookie header is definitely there in the response coming back from the Microsoft redirect, but then on the next request the browser just... doesn't send it. I'm not sure if the SameSite=Strict thing is relevant to that but it seemed worth mentioning.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra ID SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft, get redirected back to TaskFlow, but the session cookie does not persist, causing TaskFlow to immediately redirect them back to Microsoft. Affected users correlate strongly with those who have plus-addressed emails (e.g. jane+taskflow@company.com) or emails that were changed/aliased during onboarding.","root_cause_hypothesis":"Two interacting factors are likely at play. First, the session cookie is set with SameSite=Strict, which causes browsers to not send the cookie on the initial cross-site navigation back from Microsoft's IdP — this may affect all users on first redirect but is recoverable for most. Second, for users with plus-addressed or aliased emails, TaskFlow's user lookup or session creation likely fails because the email claim in the Entra ID token (containing a '+' or a different email variant) doesn't match the stored user record. This could be due to (a) the '+' being URL-decoded or stripped during claim parsing ('+' → space is a common encoding issue), (b) a case-sensitive or exact-match email comparison failing for aliased users, or (c) the cookie value itself being corrupted by the '+' character if the email is embedded in the session token before encoding. The combination of SameSite=Strict preventing session persistence on cross-site redirect AND a failed user lookup preventing session creation produces the persistent loop.","reproduction_steps":["Set up a user in Entra ID with a plus-addressed email (e.g. testuser+taskflow@company.com) and grant them access to the TaskFlow app registration","Ensure no existing TaskFlow session cookies exist (clear cookies or use incognito)","Navigate to TaskFlow login and initiate SSO login","Authenticate successfully at Microsoft Entra ID","Observe the redirect back to TaskFlow — check for Set-Cookie header with taskflow_session/tf_session","Observe that the browser does not send the cookie on the subsequent request, and TaskFlow redirects back to Microsoft","Repeat with a plain email user (no plus-addressing, never changed) to confirm they do not experience the loop"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Session cookie uses SameSite=Strict, Path=/. Cookie name is taskflow_session or tf_session. Affects users with plus-addressed emails and users whose emails were changed/aliased post-onboarding.","severity":"high","impact":"Approximately 30% of users are completely unable to log into TaskFlow. No workaround is currently known. Affected users can authenticate to other SSO apps fine, so the problem is TaskFlow-specific.","recommended_fix":"Investigate in this order: (1) Check how TaskFlow parses the email/UPN claim from the Entra ID token — log the raw claim value for a plus-address user and compare it against the user lookup query. Look for URL encoding issues where '+' becomes a space. (2) Change the session cookie from SameSite=Strict to SameSite=Lax — Strict is incompatible with IdP-initiated cross-site redirects and Lax is the standard setting for SSO callback flows. (3) If the user lookup uses exact email matching, normalize both sides (lowercase, handle plus-addressing) or match on a more stable identifier like the Entra ID object ID (oid claim) rather than email. (4) For aliased users, ensure TaskFlow checks all known email aliases or uses the primary UPN from the token rather than a cached email from the old Okta setup.","proposed_test_case":"Create integration tests for the SSO callback handler that exercise: (a) a token with a plus-addressed email claim — verify user lookup succeeds and session is created, (b) a token where the email claim differs from the stored email by case or alias — verify matching still works, (c) verify the Set-Cookie header on the callback response uses SameSite=Lax (not Strict).","information_gaps":["Exact server-side logs showing what happens during the callback — whether user lookup fails or session creation fails","Result of the alias test (giving a plus-address user a plain email to confirm the plus sign is causal)","Whether the few ambiguous affected users (not clearly plus-addressed or aliased) represent a third pattern or are actually aliased users the reporter hasn't confirmed yet","Whether any users who successfully log in also have plus-addresses (which would weaken the hypothesis)","Exact cookie name (taskflow_session vs tf_session) — reporter was unsure"]}

---
