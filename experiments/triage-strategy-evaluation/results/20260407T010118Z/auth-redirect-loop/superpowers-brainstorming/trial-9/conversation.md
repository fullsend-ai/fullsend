# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. To help narrow this down — do the affected users share anything in common that the unaffected users don't? For example:

1. They had active/cached sessions in TaskFlow before the SSO switch
2. They belong to a different group, role, or OU in your directory
3. They're using a specific browser or device type
4. Something else you've noticed

This will help us figure out whether this is a stale session/cookie issue vs. a configuration mismatch for certain user attributes.

---

### reporter

Good question. I've been trying to figure out the pattern myself. It doesn't seem to be browser-specific — we've seen it on Chrome, Edge, and Firefox. And it's not a group/role thing either, the affected users are spread across different teams.

One thing I did notice is that a lot of the affected users have those plus-sign email addresses, like jane+taskflow@company.com. We set those up a while back so people could filter their TaskFlow notifications. But I'm not 100% sure that accounts for all the affected users — there might be a couple who don't use plus addressing but still have the issue.

Also, clearing cookies and trying again sometimes helps temporarily, and a couple people said incognito mode worked for them, which is weird. Not sure if that's relevant.

---

### triage-agent

Thanks — the plus-sign email pattern is a really strong clue. Here's what I think is happening:

When Entra ID redirects back to TaskFlow, it's likely encoding the `+` in addresses like `jane+taskflow@company.com` differently than Okta did (e.g., as `%2B` or as a space). TaskFlow then can't match the authenticated identity to the local user record, so it treats the user as unauthenticated and kicks them back to the login flow. The cookie/incognito behavior fits too — stale session cookies from the old Okta setup may be interfering with the new flow.

Could you check one thing to confirm? In TaskFlow's authentication logs (or your Entra ID sign-in logs), when an affected user completes the redirect loop, does the email claim coming back from Entra ID look different from what's stored in TaskFlow — specifically around the `+` character being encoded or stripped?

---

### reporter

I checked the Entra ID sign-in logs and it looks like for those plus-address users, Entra is sending back their primary email without the plus part — so jane+taskflow@company.com shows up as just jane@company.com in the token. That's different from what Okta was sending. So yeah, the emails definitely don't match what's stored in TaskFlow.

For the couple of affected users who don't use plus addressing, I noticed they had their emails changed at some point — like they originally signed up with a personal email and we later aliased their company email in the directory. Not sure if that's related but figured I'd mention it.

I don't have access to TaskFlow's internal auth logs though, just the Entra side.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop: Entra ID email claims don't match TaskFlow user records (plus-addressing stripped, aliased emails differ)","problem":"After migrating from Okta to Microsoft Entra ID, ~30% of users experience an infinite redirect loop during login. Entra ID returns email claims that differ from what TaskFlow has stored — specifically, plus-addressed emails have the plus part stripped, and users with aliased/changed emails get a different primary email than what's on file. TaskFlow fails to match the identity and redirects back to SSO.","root_cause_hypothesis":"TaskFlow's auth callback does a strict match between the email claim in the OIDC token and the stored user email. Entra ID normalizes emails differently than Okta: it strips plus-addressing (jane+taskflow@company.com → jane@company.com) and may return a different primary email for users with directory aliases. The strict match fails, so TaskFlow treats the user as unauthenticated and re-initiates the SSO flow. Stale Okta session cookies may compound the issue for some users.","reproduction_steps":["Create a TaskFlow user with a plus-addressed email (e.g., jane+taskflow@company.com)","Configure Entra ID as the SSO provider with the user's primary email set to jane@company.com","Attempt to log into TaskFlow via SSO","Observe that Entra ID returns jane@company.com in the email claim","TaskFlow fails to match this to the stored jane+taskflow@company.com and redirects back to SSO","Redirect loop occurs"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected (Chrome, Edge, Firefox). Issue is identity-provider-level, not browser-specific.","severity":"high","impact":"~30% of users are completely unable to log into TaskFlow. Workarounds (incognito mode, clearing cookies) are unreliable and temporary.","recommended_fix":"1. Update TaskFlow's auth callback to normalize email comparison — strip plus-addressing and compare case-insensitively, or look up users by multiple email fields. 2. Consider matching on a stable claim like `sub` (subject ID) or `oid` (Entra object ID) rather than email, which is inherently mutable. 3. Add a one-time migration step to update stored user emails to match what Entra ID returns, or add an email-alias lookup table. 4. Invalidate pre-migration Okta session cookies to eliminate stale-session interference.","proposed_test_case":"Test SSO login with a user whose TaskFlow-stored email is jane+taskflow@company.com but whose Entra ID email claim returns jane@company.com. Verify the user is successfully authenticated and matched to their TaskFlow account without a redirect loop.","information_gaps":["Exact identity claim TaskFlow uses for user matching (email vs. sub vs. UPN) — determinable from code inspection","Whether the non-plus-addressed affected users' stored emails differ from their Entra ID primary email in a specific pattern — the aliasing explanation is plausible but unconfirmed for all cases"]}

---
