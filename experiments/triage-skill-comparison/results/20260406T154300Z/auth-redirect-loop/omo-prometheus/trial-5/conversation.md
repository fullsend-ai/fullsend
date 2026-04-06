# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — the 30% split is a strong signal that something systematic is different about those accounts.

Can you identify any pattern that distinguishes the affected users from those who can log in fine? Specifically:

- Were the affected users' accounts provisioned differently in Entra ID (e.g., migrated from Okta vs. created fresh)?
- Do the affected users belong to a different group, role, or organizational unit?
- Does clearing browser cookies and all cached TaskFlow/Okta session data resolve the loop for any of them?

Even a partial answer here would help — if you're not sure, could you pick two affected users and two unaffected users and compare their Entra ID account properties side by side?

---

### reporter

Good questions. Let me think about what's different about the affected users.

For provisioning — all accounts were migrated from Okta, none were created fresh in Entra. So that's the same across the board.

I did notice something though: a lot of the affected users are ones who use plus-addressed emails (like jane+taskflow@company.com), and a few others are people whose email addresses changed at some point — like they had a different email originally and their current one was added as an alias later. The users who can log in fine tend to be ones with straightforward emails that haven't changed.

As for clearing cookies — yes, actually! A couple of affected users found that if they clear all their cookies and try again quickly, they can sometimes get in. One person said using an incognito window worked for them too, though not consistently. But it's not a reliable fix — most of the time they still end up in the loop.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra ID migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are caught in an infinite authentication redirect loop. Users authenticate successfully with Entra ID but TaskFlow immediately redirects them back to the IdP. Affected users correlate with plus-addressed emails (user+taskflow@company.com) and users whose email addresses changed (aliases). Clearing cookies or incognito mode occasionally breaks the loop temporarily.","root_cause_hypothesis":"TaskFlow matches authenticated users by comparing the email claim in the OIDC/SAML token against stored user records. Okta likely returned the plus-addressed or alias email in the token's email claim (matching what TaskFlow stored), while Entra ID returns the canonical primary email from the directory. When TaskFlow receives an email claim that doesn't match any stored user, it cannot establish a session and redirects back to the IdP, creating the loop. The intermittent cookie/incognito fix suggests residual Okta session cookies may also interfere with the new auth flow.","reproduction_steps":["Set up a user account in TaskFlow with a plus-addressed email (e.g., user+taskflow@company.com)","Ensure that user's Entra ID primary email is the base address (user@company.com)","Attempt to log into TaskFlow via SSO","Observe: Entra ID authentication succeeds, redirect back to TaskFlow occurs, but TaskFlow immediately redirects to Entra ID again in a loop"],"environment":"TaskFlow with SSO integration, recently migrated from Okta to Microsoft Entra ID. All user accounts were migrated (none created fresh in Entra). Redirect URIs verified correct in Entra app registration.","severity":"high","impact":"~30% of users are completely unable to log into TaskFlow. No reliable workaround exists — clearing cookies and incognito mode work only intermittently. Affected users can authenticate with other Entra ID apps, so this is blocking only TaskFlow access.","recommended_fix":"1. Inspect the OIDC/SAML token claims from Entra ID for an affected user vs. an unaffected user — compare the 'email', 'preferred_username', and 'upn' claims. 2. Check which claim TaskFlow uses for user matching (likely 'email') and what value is stored in the TaskFlow user table for affected accounts. 3. Fix the mismatch by either: (a) configuring Entra ID to send the plus-addressed/alias email in the token claim (via optional claims or claims mapping policy), or (b) updating TaskFlow's user-matching logic to normalize emails (strip plus-addressing, check aliases) or match on a stable identifier like 'sub' or 'oid' instead of email. 4. Clear any residual Okta session cookies by invalidating old session tokens or updating the session cookie domain/name.","proposed_test_case":"Create test users with three email scenarios: (1) standard email, (2) plus-addressed email, (3) aliased/changed email. Authenticate each via Entra ID SSO and verify all three successfully establish a TaskFlow session without redirect loops. Additionally, verify that a user with stale Okta cookies in their browser can authenticate cleanly.","information_gaps":["Exact OIDC/SAML claim configuration in TaskFlow's auth module (which claim is used for user matching)","Whether TaskFlow uses 'email', 'upn', 'preferred_username', or 'sub' as the user identifier","Entra ID optional claims configuration for the TaskFlow app registration","Whether any affected users lack the plus-address or alias pattern (could indicate additional causes)"],"confidence":{"root_cause":"high","reproduction":"medium","severity_assessment":"high"}}

---

