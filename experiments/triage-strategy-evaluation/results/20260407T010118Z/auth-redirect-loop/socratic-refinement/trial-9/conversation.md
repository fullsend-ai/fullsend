# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — this is really helpful context.

I'd like to understand what sets the affected users apart from those who can log in fine. Have you noticed any pattern among that ~30%? For example:

- Were they actively logged into TaskFlow (with existing sessions) at the time of the SSO switch, while others weren't?
- Do they share anything in common — same browser, same OS, same office location, same role or group membership in Entra ID?

Even a hunch here would help narrow things down. Sometimes the pattern isn't obvious until you start looking at who specifically is affected.

---

### reporter

Good question. I looked into this a bit and I don't think it's about existing sessions — we had everyone log out before the switch. It's not browser or OS specific either, we've seen it on Chrome, Edge, and Firefox.

One thing I did notice is that a lot of the affected users have those plus-sign email addresses, like jane+taskflow@company.com. We use that plus addressing convention for some of our team. I'm not 100% sure that's the only pattern though — there might be a couple users affected who don't have the plus sign. Some of our people also had their email addresses changed or aliased when we set up Entra, so that could be a factor too.

Oh and one weird thing — a couple people said they were able to get in using an incognito window, but it doesn't work consistently.

---

### triage-agent

That plus-sign pattern is a really interesting lead — and the email aliasing detail might be connected too.

Can you tell me more about what happened with the email changes during the Entra setup? Specifically, for users like `jane+taskflow@company.com` — is that the email address registered in Entra ID as their primary address, or did Entra end up storing a different form of it (like `jane@company.com` without the plus part, or a new alias)?

I'm trying to understand whether the email that Entra sends back to TaskFlow after authentication might not match the email TaskFlow has stored for that user — which could explain the loop if TaskFlow sees a successful auth but can't match it to an account, and kicks the user back to re-authenticate.

---

### reporter

So in Entra ID, the plus-addressed emails aren't set up as the primary email — Entra uses the base email like jane@company.com as the primary. In Okta we had it configured to send the full plus address, but I'm not sure Entra is doing the same thing. I haven't dug into the actual SAML claims or anything like that, but it would make sense that there's a mismatch there.

For the aliased users, it's a similar situation — some people had their emails consolidated or changed during the migration, so the email in Entra might not match what TaskFlow originally had on file for them.

That theory about TaskFlow not being able to match the account sounds plausible to me. Though what's weird is that if it couldn't find the account, I'd expect an error message rather than an infinite loop, you know?

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop for users whose SSO email claim doesn't match stored TaskFlow email after Okta→Entra migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully in Entra, get redirected back to TaskFlow, and are immediately sent back to Entra. The affected users disproportionately have plus-sign email addresses (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during the Entra migration.","root_cause_hypothesis":"Entra ID is returning the base email (jane@company.com) in its SAML/OIDC claims, while TaskFlow has the plus-addressed email (jane+taskflow@company.com) stored as the user identifier. When TaskFlow receives a successful authentication but cannot match the returned email to a local account, it fails silently — instead of showing an error, it redirects the user back to the login flow, which triggers another SSO redirect, creating the loop. The same mismatch likely affects users whose emails were consolidated or aliased during migration. The Okta configuration explicitly sent the full plus-addressed email in claims; the Entra configuration does not.","reproduction_steps":["Have a TaskFlow account registered with a plus-addressed email (e.g., user+taskflow@company.com)","Ensure Entra ID has the user's primary email set to the base form (user@company.com)","Attempt to log into TaskFlow via SSO","Observe: Entra authentication succeeds, redirect back to TaskFlow occurs, then immediate redirect back to Entra, repeating indefinitely"],"environment":"TaskFlow with SAML/OIDC SSO, recently migrated from Okta to Microsoft Entra ID. Affects Chrome, Edge, and Firefox. Not browser- or OS-specific.","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely. Workarounds (incognito windows) are unreliable. Blocks daily work for affected users.","recommended_fix":"Two-pronged investigation: (1) **Claims configuration:** Check what email claim Entra ID is sending back (inspect SAML assertions or OIDC tokens). Configure Entra to send the full plus-addressed email, or add custom claims mapping to match what Okta sent. (2) **Account matching logic:** Fix TaskFlow's SSO callback handler to handle email mismatches gracefully — when authentication succeeds but no matching account is found, display an error page instead of redirecting to login. Consider implementing case-insensitive, alias-aware email matching (e.g., treating user+tag@domain and user@domain as potential matches). Also audit any users whose emails changed during migration and update TaskFlow records or Entra claims accordingly.","proposed_test_case":"Create a test user in TaskFlow with email user+tag@company.com. Configure Entra to return user@company.com in the email claim. Attempt SSO login and verify: (a) the user is either matched correctly via alias-aware logic, or (b) a clear error message is displayed — not a redirect loop. Additionally, test with a user whose email matches exactly to confirm no regression.","information_gaps":["Exact SAML/OIDC claim values Entra is returning (would confirm the mismatch definitively — developer can check server logs or inspect tokens)","Why incognito windows sometimes work (may relate to cached Okta session cookies interfering, or timing-dependent token state)","Whether TaskFlow's SSO callback code has explicit error handling for unmatched accounts or silently falls through to a redirect"]}

---
