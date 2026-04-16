# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the 30% figure is really interesting and probably the key to cracking this.

Have you noticed any pattern at all among the affected users versus the ones who can log in fine? For example:
- Had the affected users logged into TaskFlow recently (before the SSO switch), while the unaffected ones hadn't?
- Are they on a particular browser, or using a different device type?
- Do they belong to a different group, role, or organizational unit in your directory?

Even a hunch or something you've half-noticed would be helpful here — sometimes the pattern isn't obvious until you say it out loud.

---

### reporter

Good question — I've been trying to figure out the pattern myself. It's not browser-specific, we're seeing it on Chrome, Edge, and Firefox. And it's not really about roles or groups either.

What I *have* noticed is that the affected users tend to be the ones who use plus-addressed emails — like jane+taskflow@company.com. We have a bunch of people who set that up for filtering purposes. There are also a few users who had their emails changed or aliased at some point during the Okta-to-Entra migration. The users who log in fine seem to be the ones with straightforward email addresses that haven't changed.

Oh, one other thing that might be relevant — a couple of the affected users found that if they clear all their cookies and try again quickly, or use an incognito window, they can sometimes get in. But it's not consistent.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Entra ID but are immediately redirected back to the login flow. The affected users predominantly use plus-addressed emails (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during the migration.","root_cause_hypothesis":"TaskFlow's authentication callback matches the email claim returned by Entra ID against locally stored user identifiers. Entra ID likely returns the canonical email address (stripping the plus suffix) or a different alias than what Okta returned and TaskFlow stored. When the email claim doesn't match any stored user record, TaskFlow cannot establish a session and falls back to the login flow, creating the loop. Stale session cookies from the Okta integration may also interfere with the new auth flow, explaining why clearing cookies occasionally helps.","reproduction_steps":["Have a user account in TaskFlow whose stored email uses plus-addressing (e.g., user+taskflow@company.com) or was aliased during migration","Configure TaskFlow SSO to use Microsoft Entra ID","Attempt to log in with that user's credentials","Observe successful Entra ID authentication followed by redirect back to TaskFlow login, repeating in a loop"],"environment":"TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox equally. Not role or group dependent.","severity":"high","impact":"Approximately 30% of the team cannot reliably log into TaskFlow. No consistent workaround exists — clearing cookies and incognito mode work only intermittently.","recommended_fix":"1. Inspect the email claim Entra ID returns in the SAML assertion or OIDC token (likely the `email` or `preferred_username` claim) and compare it against what TaskFlow stores for affected users. 2. Normalize email comparison in the auth callback: strip plus-address suffixes and perform case-insensitive matching, or match on a stable identifier (like the `sub` or `oid` claim) rather than email. 3. Add a user identity migration step that maps old Okta identifiers to new Entra ID identifiers. 4. Invalidate or ignore pre-migration session cookies by rotating the session signing key or bumping a session version.","proposed_test_case":"Create test users with plus-addressed emails and aliased emails. Simulate an SSO callback with an email claim that differs from the stored identifier (canonical vs plus-addressed). Verify that the auth callback correctly resolves the user, establishes a session, and does not redirect back to login.","information_gaps":["Exact email claim name and value Entra ID returns for affected users versus what TaskFlow has stored","Whether TaskFlow uses email as the sole user identifier or also stores a subject/object ID","Whether the Okta integration used a different claim format that plus-addresses were preserved in"]}

---
