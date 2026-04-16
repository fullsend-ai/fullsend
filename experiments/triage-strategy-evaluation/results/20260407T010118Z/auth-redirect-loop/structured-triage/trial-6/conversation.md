# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the redirect loop behavior and the 30% affected rate are both helpful starting points.

Could you share some environment details? Specifically:
- What browser(s) are the affected users using? Have you noticed whether the issue is browser-specific (e.g., only Chrome, or only Firefox)?
- What OS are they on?
- What version of TaskFlow are you running?

Also, have you noticed any pattern that distinguishes the affected 30% from the rest — for example, are they on a different office/VPN, using different browsers, or were their accounts migrated differently during the SSO switch?

---

### reporter

The affected users are spread across Chrome, Edge, and Firefox — it doesn't seem to be browser-specific. Most of our team is on Windows 10/11, a few on Macs, and it happens on both. We're on TaskFlow v2.3.1, self-hosted.

As for a pattern, I've been trying to figure that out. I noticed that a few of the affected users have those plus-sign email addresses, like jane+taskflow@company.com — we use those for some people. But I'm not sure that accounts for all of them. Some of the affected users I think had their emails changed or aliased at some point during the migration. The ones who can log in fine seem to just have straightforward email addresses. I haven't been able to pin down a single cause though.

Oh, one thing — a couple of users said they were able to get in using an incognito window, but it doesn't always work. Not sure if that's relevant.

---

### triage-agent

Thanks for the thorough follow-up — the plus-sign email pattern and the incognito window clue are both really interesting leads.

Could you check the TaskFlow server-side logs for one of the affected users during a login attempt? Specifically, I'm looking for:
- Any error or warning messages logged during the redirect loop (authentication failures, token validation errors, etc.)
- Whether the logs show the email address that Entra ID is passing back to TaskFlow — and whether it matches what TaskFlow has stored for that user

The plus-sign and aliased email observation is a strong hint that there may be an email normalization mismatch between what Entra ID returns and what TaskFlow expects, and the logs should help confirm or rule that out.

---

### reporter

I'll take a look at the logs — since we're self-hosted I should be able to pull those up. Give me a bit to find the right log entries. I did poke around in there briefly yesterday and I remember seeing something about session creation happening repeatedly for one of the affected users, like it was trying to set up their session over and over. I think I also saw the email coming back from Entra was different from what was stored — for one of the plus-address users, it looked like Entra was sending back just their plain email without the plus part. I didn't dig much deeper than that though.

I'm not sure where exactly to look for cookie or token validation errors specifically — is there a particular log file or search term you'd recommend I look for? We have a lot of log output and I'm not super familiar with TaskFlow's logging setup.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop due to email normalization mismatch between Entra ID and TaskFlow","problem":"After migrating from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop when logging in. Users authenticate successfully with Microsoft but are immediately redirected back to the login page repeatedly. The issue correlates with users who have plus-sign email addresses (e.g., jane+taskflow@company.com) or emails that were changed/aliased during the SSO migration.","root_cause_hypothesis":"Entra ID normalizes or strips plus-sign email addresses when returning claims (e.g., returning jane@company.com instead of jane+taskflow@company.com). TaskFlow's user lookup compares the SSO-returned email against its stored email using an exact match. When they differ, the lookup fails, no session is created, and the user is redirected back to the SSO provider, creating the loop. Okta likely preserved the full plus-sign address in its claims, masking this mismatch before the migration.","reproduction_steps":["Set up a TaskFlow user account with a plus-sign email address (e.g., testuser+taskflow@company.com)","Configure Entra ID SSO for TaskFlow","Attempt to log in as that user via SSO","Observe: authentication succeeds at Microsoft, but TaskFlow redirects back to Microsoft in a loop","Check server logs to confirm the email returned by Entra ID differs from the stored email"],"environment":"TaskFlow v2.3.1, self-hosted. Windows 10/11 and macOS. Reproduced on Chrome, Edge, and Firefox. SSO provider: Microsoft Entra ID (recently migrated from Okta).","severity":"high","impact":"Approximately 30% of the team is completely unable to log in to TaskFlow. These users are locked out of the application entirely, with no reliable workaround (incognito windows work intermittently).","recommended_fix":"Investigate the SSO callback handler where the email claim from Entra ID is matched against stored user records. Likely fix: normalize emails before comparison (strip plus-sign suffixes, case-fold, or match on the base address). Alternatively, add a secondary lookup by alias or external SSO identifier (sub/oid claim) rather than relying solely on email matching. Also check whether the session creation code is failing silently on the lookup miss instead of returning an auth error, which is what causes the loop rather than a clear error page.","proposed_test_case":"Create a user with a plus-sign email (user+tag@example.com). Simulate an SSO callback where the identity provider returns the plain email (user@example.com). Verify that the user is matched correctly and a session is created. Also test with aliased/changed emails to ensure the lookup is resilient to common email variations.","information_gaps":["Exact log entries showing the email mismatch and session creation failure (reporter confirmed seeing this but hasn't provided verbatim logs)","Whether all affected users have plus-sign or aliased emails, or if there are other patterns","Which Entra ID claim TaskFlow is using for email matching (email, preferred_username, or UPN)","Whether the incognito workaround correlates with cached stale session cookies from the old Okta integration"]}

---
