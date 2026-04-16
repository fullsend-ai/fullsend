# Conversation: auth-redirect-loop x omo-prometheus

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report — that 30% split is a really useful clue.

What distinguishes the affected users from the ones who can log in fine? Specifically:

- Do the affected users have anything in common? (e.g., different department/group, different role in TaskFlow, accounts created at a different time, different browser or OS)
- Have the affected users tried clearing their browser cookies/cache for TaskFlow and attempting login again in a fresh incognito/private window?

Knowing whether this correlates with a user attribute vs. a client-side state issue will help narrow this down quickly.

---

### reporter

Good questions. So I've been looking into what the affected users have in common, and I noticed a pattern — a lot of them use plus-addressed emails (like jane+taskflow@company.com), and a few others had their email addresses changed or aliased when we migrated to Entra ID. The users who can log in fine seem to be the ones whose primary email is straightforward and matched exactly between Okta and Entra.

As for clearing cookies — yes, a few people have tried that. It's inconsistent. Sometimes clearing all cookies and trying again quickly lets them in, and a couple of people said incognito/private browsing worked for them at least once. But it's not a reliable fix, and the problem comes back.

---

### triage-agent

Thanks — that plus-addressing and email alias pattern is a strong signal. Here's my working theory:

TaskFlow is matching the email claim from the Entra ID token against stored user records using an exact string comparison. For users with plus-addressed emails (like `jane+taskflow@company.com`) or emails that changed during the migration, the email in the token doesn't exactly match what TaskFlow has on file — so the auth callback fails to find the user, can't establish a session, and kicks them back to the login flow.

To confirm this, could you check two things?

1. **TaskFlow's auth/application logs** during a failed login attempt from an affected user — is there an error or warning about user lookup failing, an unrecognized email, or a session not being created?
2. **What email does the Entra ID token actually contain for an affected user?** Specifically, is Entra sending back their primary/canonical email (e.g., `jane@company.com`) or the plus-addressed version (`jane+taskflow@company.com`)? You can usually see this in the Entra ID sign-in logs under the token claims.

This will tell us whether the mismatch is on the identity provider side (Entra sending a different email than expected) or the application side (TaskFlow doing a strict match against stale records).

---

### reporter

I checked the Entra ID sign-in logs for one of our affected users — Jane, who uses jane+taskflow@company.com. In the token claims, Entra is sending back jane@company.com (without the plus part). So yeah, there's definitely a mismatch there. Okta used to send the full plus-addressed email.

As for TaskFlow's logs — I'm not a developer so I don't have deep access to the application logs, but I did ask our dev team to take a quick look. They mentioned seeing the auth callback fire but the session not sticking. One thing they pointed out is that there IS a Set-Cookie header in the redirect response coming back from the SSO flow, but for some reason the cookie doesn't seem to actually get saved in the browser. They could see it in the network tab of dev tools — the cookie is there in the response headers but it's just not being set. I don't know enough to explain why that would happen, but it seems like there might be two things going on — the email mismatch you described, plus something weird with the cookie not being saved during the redirect back from Microsoft.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Microsoft but are immediately redirected back to the login page. Affected users are predominantly those with plus-addressed emails (e.g., jane+taskflow@company.com) or emails that were aliased/changed during the Entra migration.","root_cause_hypothesis":"Two compounding issues: (1) **Email claim mismatch**: Entra ID returns the user's canonical/primary email in the token (e.g., jane@company.com), whereas Okta returned the plus-addressed form (jane+taskflow@company.com). TaskFlow's user lookup in the auth callback uses exact string matching against the stored email, so it fails to find the user. (2) **SameSite cookie issue on cross-origin redirect**: The session cookie's SameSite attribute is likely set to 'Lax' or 'Strict' (or defaulting to 'Lax' per modern browser defaults). When the browser is redirected back from Microsoft's domain to TaskFlow, this is a cross-site navigation, and the browser refuses to persist the cookie. This means even if user lookup succeeded, the session wouldn't stick — causing the loop. The two issues may affect overlapping but distinct subsets of the ~30% affected users.","reproduction_steps":["Have a user account in TaskFlow whose stored email uses plus-addressing (e.g., jane+taskflow@company.com)","Ensure the corresponding Entra ID account's primary email is the canonical form (jane@company.com)","Attempt to log in to TaskFlow via SSO","Observe: user authenticates with Microsoft successfully, is redirected back to TaskFlow, but is immediately redirected to Microsoft again in a loop","Check browser dev tools Network tab: confirm the auth callback response contains a Set-Cookie header but the cookie is not saved by the browser"],"environment":"TaskFlow web application with SSO authentication, recently migrated from Okta to Microsoft Entra ID. Multiple browsers affected. The issue is not browser-specific but incognito/cookie-clearing provides inconsistent temporary relief.","severity":"high","impact":"~30% of users are completely unable to log into TaskFlow reliably. No dependable workaround exists — clearing cookies and incognito mode are inconsistent. This is a production blocker for affected users.","recommended_fix":"Address both root causes: (1) **Email matching**: Modify TaskFlow's auth callback to normalize email comparison — strip plus-address suffixes and/or perform case-insensitive lookup. Better yet, match users on a stable unique identifier (like the Entra Object ID / `sub` or `oid` claim) rather than email, and add a one-time migration to populate that field from current sessions or a bulk mapping. As a quick fix, update the stored emails for affected users to match what Entra sends. (2) **Session cookie**: Set the session cookie's SameSite attribute to `None` and ensure the `Secure` flag is set (required for SameSite=None). Verify the cookie's Domain and Path attributes are correct for the redirect target URL. Check that the response also includes appropriate CORS headers if needed.","proposed_test_case":"Create a test user with a plus-addressed email stored in TaskFlow's database. Mock the SSO token to return the canonical (non-plus) email. Verify that the auth callback successfully matches the user, creates a valid session, and the session cookie is persisted across the redirect. Confirm no redirect loop occurs. Additionally, write a unit test for the email normalization logic to ensure plus-addressing, case differences, and alias variations all resolve to the correct user.","information_gaps":["Exact SameSite and Secure attribute values currently set on TaskFlow's session cookie","Whether TaskFlow uses OIDC or SAML with Entra (affects redirect method: POST vs GET, which impacts SameSite behavior differently)","Whether any reverse proxy or CDN in front of TaskFlow is stripping or modifying Set-Cookie headers","Full application-level auth logs showing the exact failure point in the callback handler"]}

---
