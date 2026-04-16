# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. The fact that only ~30% of users are affected is a strong clue — understanding what makes them different should point us toward the root cause.

Do you notice any pattern among the affected users? For example:

1. **They existed before the SSO migration** and had active sessions or cached credentials with Okta
2. **They belong to a different group or role** in Entra ID (e.g., different security group, different license tier)
3. **They're using a specific browser or device** (e.g., all on Firefox, or all on managed corporate devices)
4. **Something else** you've noticed

Even a rough pattern would help narrow this down significantly.

---

### reporter

Good question. I've been trying to figure out a pattern myself. It's not browser-specific — we've seen it on Chrome, Edge, and Firefox. And it's not a group/role thing either, we checked that.

One thing I did notice is that a lot of the affected users have plus-addressed emails — like jane+taskflow@company.com. We use that convention for some of our team. But I'm not 100% sure that's the only factor because I think there are a couple affected users who don't have the '+' in their email. Those might be people whose email changed at some point though, like we had a few people whose emails were set up as aliases in Entra because they had a different primary email in Okta.

Also, I noticed that sometimes affected users can get in if they use an incognito window, which is weird. It doesn't always work but it's worked a few times.

---

### triage-agent

Thanks, this is really helpful — the plus-addressed email pattern is a strong signal. Let me propose a hypothesis and see if it fits:

I think the redirect loop is happening because **Entra ID is returning a different email in the OIDC token than what TaskFlow has stored for these users.** For example, if a user is `jane+taskflow@company.com` in TaskFlow's database, but Entra ID sends back `jane@company.com` (the primary/canonical email) in the `email` or `preferred_username` claim, TaskFlow can't match them to an existing account and restarts the login flow — causing the loop. The same thing would explain the users without `+` who had email changes during migration.

Could you check one thing to confirm or rule this out? If you look at the Entra ID token for an affected user (the easiest way is usually the "Enterprise Applications > Sign-in logs" blade in Azure portal), **does the email claim in the token match exactly what's stored in TaskFlow's user table for that person?** Even a subtle difference (casing, alias vs. primary, with/without the `+` portion) would explain everything you're seeing.

---

### reporter

Oh that's a really good theory. I just checked the sign-in logs in Azure for one of our plus-addressed users (jane+taskflow@company.com) and yeah, the token is coming back with just jane@company.com in the email claim. So there's definitely a mismatch there. I haven't checked the aliased users yet but I'd bet it's the same kind of thing — the email Entra is sending doesn't match what TaskFlow has on file.

That said, one thing that still confuses me is why the incognito window thing sometimes works. If it was purely an email mismatch, wouldn't it fail every time regardless of the browser mode? I don't know if that's relevant but it seemed odd to me.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users whose stored email doesn't match Entra ID email claim (plus-addressing and alias mismatches)","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. The user authenticates successfully with Entra ID but TaskFlow cannot match the returned identity to an existing account, causing it to restart the login flow. The affected population consists primarily of users with plus-addressed emails (e.g., jane+taskflow@company.com) and users whose primary email changed during the migration (Okta alias vs. Entra primary email).","root_cause_hypothesis":"TaskFlow's OIDC callback handler performs an exact-match lookup of the `email` (or `preferred_username`) claim from the Entra ID token against its user table. Entra ID returns the user's primary/canonical email (jane@company.com), which does not match the plus-addressed or aliased email stored in TaskFlow (jane+taskflow@company.com). The lookup fails, and instead of surfacing an error, the application re-initiates the SSO flow — creating an infinite redirect loop. The stale session cookie from each failed attempt likely contributes to the loop persisting in non-incognito browsers, which explains why incognito occasionally breaks the cycle.","reproduction_steps":["Have a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com) or an email that differs from their Entra ID primary email","Attempt to log in via the Microsoft Entra ID SSO flow","Authenticate successfully in Entra ID","Observe that TaskFlow redirects back to Entra ID instead of completing login, creating an infinite loop"],"environment":"TaskFlow with OIDC/SSO integration, recently migrated from Okta to Microsoft Entra ID. Affects all browsers (Chrome, Edge, Firefox). Approximately 30% of users affected.","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely (unless they happen to get lucky with incognito). This is a blocking issue for daily work for those users.","recommended_fix":"1. In the OIDC callback handler, change the user lookup to normalize emails before matching — at minimum, strip the plus-addressed portion (everything between `+` and `@`) before comparing. 2. Additionally, consider storing the Entra ID `sub` (subject) claim as a stable user identifier rather than relying solely on email matching, since emails can change across IdP migrations. 3. As an immediate workaround, update the affected users' emails in TaskFlow's database to match what Entra ID returns, or configure Entra ID to emit the plus-addressed email via a custom claim mapping.","proposed_test_case":"Create test users with plus-addressed emails (user+tag@domain.com) and emails that differ from the IdP's primary email. Verify that the OIDC callback correctly matches these users to their accounts after authentication. Also verify that when no match is found, the application surfaces a clear error rather than entering a redirect loop.","information_gaps":["Exact OIDC claim TaskFlow uses for user lookup (email vs. preferred_username vs. sub) — inspecting the codebase will clarify this","Whether TaskFlow has a user-creation-on-first-login flow that might interact with the mismatch","The precise behavior difference in incognito — likely just clean cookie state, but could confirm by inspecting the redirect chain with DevTools"]}

---
