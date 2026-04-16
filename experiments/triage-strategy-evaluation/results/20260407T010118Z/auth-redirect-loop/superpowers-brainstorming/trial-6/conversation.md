# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report! This is a great starting point.

The fact that only ~30% of users are affected is a strong clue. Have you noticed any pattern that distinguishes the affected users from those who can log in fine? For example:

1. **Account migration timing** — Were affected users' accounts migrated to Entra ID differently (e.g., a separate batch, or they had existing Microsoft accounts that conflicted)?
2. **Browser/session state** — Does clearing cookies and browser cache resolve it for affected users?
3. **Group or role membership** — Are affected users in a different security group or assigned a different role in either Entra ID or TaskFlow?
4. **Something else** you've noticed?

This will help narrow down whether the issue is session/cache-related, account-configuration-related, or something else entirely.

---

### reporter

Good questions. For #2, yeah actually clearing cookies does seem to help sometimes — a couple users reported they got in after clearing everything, though I think it didn't work for everyone. One person said incognito mode worked which was weird.

For #1, I'm not sure about batch timing exactly, but I did notice something — a lot of the affected users seem to be ones who use plus-addressed emails (like jane+taskflow@company.com). We have a handful of people who do that. There are also a few users who had their email addresses changed or aliased when we moved to Entra, like people whose accounts were originally under a different email.

For #3, I don't think it's a group/role thing — we have affected users across different teams and roles.

I also noticed the UI on the login page seems to flash briefly before redirecting, if that matters at all.

---

### triage-agent

Thanks — the plus-addressing and email aliasing detail is a really strong clue. Here's what I think is happening:

When a user authenticates via Entra ID, TaskFlow receives an identity token containing an email claim. TaskFlow then tries to match that email against its stored user records to find the account. For users with plus-addressed emails (like `jane+taskflow@company.com`) or emails that changed during the migration, the email in the Entra ID token likely doesn't match what TaskFlow has on file — so TaskFlow doesn't recognize them as logged in and kicks them back to the login flow, creating the loop.

The reason clearing cookies or incognito sometimes works could be that it clears a stale Okta session cookie that's interfering with the new Entra flow, which is a separate (but related) issue.

Does this match what you're seeing? Specifically: can you check what email address Entra ID is returning in the token for one of the affected plus-address users — is it the full `jane+taskflow@company.com` or is it the base `jane@company.com`? If you can check the `preferred_username` or `email` claim in the token (or in the Entra ID user profile), that would confirm whether there's a mismatch.

---

### reporter

That explanation makes a lot of sense actually. I'm not super familiar with checking token claims directly, but I did look at the user profiles in Entra ID — for the plus-address users, their primary email in Entra is listed as just jane@company.com without the +taskflow part. So yeah, Okta was sending the full plus address but Entra is sending the base email. That would definitely be a mismatch with what TaskFlow has stored.

For the aliased users it's a similar situation — their Entra profile has their new email but TaskFlow still has the old one on file.

One thing though — if it was purely an email matching issue, wouldn't those users just get an 'account not found' error instead of a redirect loop? The loop part is what confuses me. It's like TaskFlow keeps trying over and over rather than just failing.

---

### triage-agent

[RESOLVED] {"title":"SSO login redirect loop for users whose email in Entra ID differs from stored TaskFlow email (plus-addressing and aliased accounts)","problem":"After migrating from Okta to Microsoft Entra ID for SSO, approximately 30% of users experience an infinite redirect loop when logging into TaskFlow. The user authenticates successfully with Entra ID, is redirected back to TaskFlow, and is immediately sent back to Entra ID again. Affected users correlate strongly with those who use plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses changed during the migration.","root_cause_hypothesis":"Two interacting issues: (1) **Email claim mismatch**: Okta sent the full plus-addressed email (jane+taskflow@company.com) as the identity claim, but Entra ID sends only the base email (jane@company.com). Similarly, migrated/aliased users have a new email in Entra but their old email in TaskFlow. When TaskFlow receives the SSO callback, it tries to match the token's email claim against its user records and fails. (2) **Missing error handling on user-not-found**: Instead of showing an error when no matching user is found after SSO callback, TaskFlow appears to redirect back to the login page, which re-initiates the SSO flow, creating the infinite loop. A secondary issue is that stale Okta session cookies may interfere with the Entra flow for some users, explaining why clearing cookies or using incognito sometimes resolves it.","reproduction_steps":["Have a user account in TaskFlow stored with a plus-addressed email (e.g., jane+taskflow@company.com) or a pre-migration email alias","Ensure the corresponding Entra ID profile has the base email (jane@company.com) or the new aliased email","Attempt to log into TaskFlow via SSO","Observe: user authenticates successfully with Entra ID, is redirected to TaskFlow, then immediately redirected back to Entra ID in a loop","Optionally: observe that the login page UI flashes briefly before each redirect"],"environment":"TaskFlow with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Entra ID app registration and redirect URIs have been verified as correct. Affects ~30% of users, cross-cutting across teams and roles.","severity":"high","impact":"~30% of users are completely unable to log into TaskFlow through normal means. Workarounds (clearing cookies, incognito) are unreliable and not viable long-term.","recommended_fix":"Two changes needed: (1) **Fix email matching logic**: Update the user lookup that runs after SSO callback to handle email normalization. Options include stripping the plus-address suffix before lookup, matching on a canonical email field, or performing a case-insensitive/alias-aware lookup. For migrated users, either update their stored emails in TaskFlow to match Entra ID, or add support for multiple email aliases per account. (2) **Fix the redirect loop**: Add proper error handling for the case where SSO authentication succeeds but no matching TaskFlow user is found — display a clear error message (e.g., 'No account found for this email, contact your admin') instead of redirecting back to login. (3) **Optional**: Invalidate or clear stale Okta session cookies during the migration period to prevent the secondary cookie-interference issue.","proposed_test_case":"Create a test user in TaskFlow with a plus-addressed email (user+tag@example.com). Configure the SSO mock/stub to return the base email (user@example.com) in the identity token. Verify that: (a) the user is successfully matched and logged in after the email matching fix, and (b) if matching still fails for any reason, an error page is shown instead of a redirect loop.","information_gaps":["Exact code path in TaskFlow's SSO callback handler that performs user lookup and the specific field/claim used for matching","Whether TaskFlow uses the 'email' or 'preferred_username' claim from the OIDC token","Whether there is an existing email alias or secondary email field in TaskFlow's user model that could be leveraged"]}

---
