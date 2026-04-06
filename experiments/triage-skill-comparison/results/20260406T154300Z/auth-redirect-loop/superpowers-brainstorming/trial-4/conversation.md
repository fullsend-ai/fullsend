# Conversation: auth-redirect-loop x superpowers-brainstorming

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the 30% affected figure is a really useful clue. To narrow down the root cause, do you notice any pattern among the affected users?

1. **They had active/recent sessions before the SSO switch** (i.e., they were logged in or had recently logged in via Okta, while unaffected users hadn't logged in recently)
2. **They belong to specific teams, roles, or permission groups** in either Entra ID or TaskFlow
3. **It's browser-specific** — affected users recover by switching browsers or using incognito/private mode
4. **Something else or no obvious pattern**

If option 3 sounds plausible but you haven't tested it yet, could you ask one or two affected users to try logging in via an incognito/private window? That would quickly tell us whether cached cookies or tokens from the old Okta setup are the culprit.

---

### reporter

Good thinking on the incognito suggestion — I actually already had a couple of affected users try that. It does sometimes work in incognito, which is weird. But it's not consistent, sometimes it still loops even there.

As for a pattern, it's not really about teams or roles. I've been staring at the list of affected users trying to figure it out, and the closest thing I've noticed is that a bunch of them use plus-addressed emails (like jane+taskflow@company.com) — we had a few people set up that way. And then there are a few others who had their email addresses changed or aliased when we migrated to Entra ID. The users whose emails are straightforward and unchanged seem to be fine.

So I guess it's kind of option 3 and option 4? The incognito thing sometimes helps, but the real pattern seems to be something about their email addresses.

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop for users with plus-addressed or migrated email addresses after Okta-to-Entra ID SSO switch","problem":"After switching SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Entra ID but TaskFlow fails to establish a session and redirects them back to the IdP. The affected users correlate with those who have plus-addressed emails (e.g., jane+taskflow@company.com) or whose email addresses were changed or aliased during the Entra ID migration.","root_cause_hypothesis":"TaskFlow's SSO callback handler matches the identity token's email claim against stored user records to find or create a session. Entra ID likely returns a normalized or canonical email (stripping the plus-address tag, or returning the new primary alias) that no longer matches the email stored in TaskFlow's user table. The mismatch causes the session lookup to fail, so TaskFlow treats the user as unauthenticated and redirects back to the IdP, creating the loop. Okta may have preserved the original plus-addressed or aliased email in its claims, masking this problem until the switch.","reproduction_steps":["Have a user account in TaskFlow whose stored email is a plus-address (e.g., jane+taskflow@company.com) or an email that was changed/aliased during Entra ID migration","Attempt to log in via SSO","Authenticate successfully at Microsoft Entra ID","Observe that TaskFlow redirects back to the IdP instead of establishing a session","The loop repeats indefinitely"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects users with plus-addressed emails or emails changed during migration. Browser-independent (incognito helps inconsistently).","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely. No reliable workaround exists — incognito mode is inconsistent. Affected users must have their email addresses manually corrected or the matching logic fixed before they can work.","recommended_fix":"1. Inspect the email claim returned by Entra ID for affected users (check the id_token or SAML assertion) and compare it to what TaskFlow has stored in its user table. 2. Fix the user-matching logic in the SSO callback to handle email normalization — either normalize plus-addresses (strip the +tag portion) before lookup, or match on a secondary/alias field. 3. For users whose emails changed during migration, ensure TaskFlow's user records are updated to reflect their current Entra ID primary email, or add support for matching on Entra ID's `oid` (object ID) claim as a stable identifier rather than relying on email alone. 4. Clear any stale Okta session cookies/tokens that may still be present (this explains the inconsistent incognito behavior).","proposed_test_case":"Create test users with plus-addressed emails (user+tag@domain.com) and aliased emails. Perform SSO login via Entra ID and verify that the callback correctly resolves them to the right TaskFlow user record and establishes a session without redirecting back to the IdP.","information_gaps":["Exact SSO protocol in use (OIDC vs SAML) — affects which claim fields to inspect","Whether TaskFlow uses email as the sole user identifier or also stores an external IdP subject/object ID","The specific email claim Entra ID is returning for plus-addressed users (stripped or preserved)"]}

---

