# Conversation: auth-redirect-loop x structured-triage

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the redirect loop behavior and the fact that only ~30% of users are affected are both really helpful starting points.

To help narrow down what's different about the affected users: have you noticed any pattern that distinguishes them from users who can log in successfully? For example:

- Were the affected users previously logged in with active sessions when the SSO switch happened?
- Do the affected users have a different role, group membership, or permission level in Entra ID?
- Are they using a different browser or device than the unaffected users?

Any pattern you've noticed (or explicitly ruled out) would help us zero in on the root cause.

---

### reporter

Good question. I've been trying to figure out the pattern myself. It doesn't seem to be browser-related — we've had people fail on both Chrome and Edge. And it's not a role or group thing either, I checked that pretty early on.

The one thing I did notice is that a lot of the affected users have email addresses with a '+' in them — we use plus addressing for some people (like jane+taskflow@company.com). I'm not 100% sure that's the only factor though, because I think there are a couple affected users who don't have the '+' but they might have had their email changed at some point. Like their Entra email might not match what they originally signed up with in TaskFlow.

Oh, and I don't think active sessions mattered — we had everyone log out before the switch.

---

### triage-agent

Thanks — the plus-addressing pattern and the email mismatch angle are both really promising leads.

To make sure a developer can reproduce and investigate this effectively, could you share some environment details?

- What version of TaskFlow are you running (and is it self-hosted or cloud-hosted)?
- Which authentication protocol are you using with Entra ID — SAML 2.0 or OpenID Connect (OIDC)?
- What OS and version is the server running on (if self-hosted)?

This will help us narrow down whether this is a known issue with a specific version or protocol handling.

---

### reporter

We're on TaskFlow v2.3.1, self-hosted. As for the authentication protocol, I'm not 100% sure — I think it's OIDC but our DevOps guy set that part up so I'd have to double-check. The server is running on Ubuntu, I want to say 22.04 but don't quote me on that.

---

### triage-agent

Thanks for all that context — the plus-addressing correlation is a really strong lead.

One more thing that would help a developer dig into this quickly: are there any relevant entries in the TaskFlow server logs when an affected user hits the redirect loop? Specifically, any authentication errors, token validation failures, or user-lookup warnings that appear during the loop.

If the logs are noisy, even a short snippet from around the time an affected user tries to log in would be helpful. Alternatively, if you open the browser's DevTools Network tab during the loop, do you see any error responses (like a 401 or 403) in the redirect chain before it bounces back to Microsoft?

---

### reporter

I haven't dug into the server logs myself honestly — I'm more on the IT admin side than the DevOps side, so I'd need to get our server guy to pull those. I can ask him though.

I did try the browser DevTools thing briefly with one of the affected users. I don't remember seeing a clear 401 or 403 — it looked like the responses were 302 redirects mostly. One thing I did notice was that there was a Set-Cookie header in one of the redirect responses, but the cookie didn't seem to stick? Like on the next request it wasn't there. I'm not sure if that's normal or not. The loop just kept going with the 302s until the browser gave up and showed an error about too many redirects.

---

### triage-agent

[RESOLVED] {"title":"SSO redirect loop after Okta-to-Entra ID migration for users with '+' in email or email mismatch","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite redirect loop when logging into TaskFlow. Users authenticate successfully with Microsoft but are immediately redirected back to the identity provider. The loop consists of 302 redirects until the browser errors with 'too many redirects'. A session cookie appears to be set during the redirect chain but does not persist on subsequent requests.","root_cause_hypothesis":"TaskFlow's user-lookup or session-creation logic likely fails when the email claim from Entra ID contains a '+' character or does not exactly match the stored user email. The most probable cause is that the email is being URL-decoded, truncated at the '+', or otherwise normalized differently than Entra ID sends it, causing the user lookup to fail silently. When the lookup fails, TaskFlow cannot establish a session (explaining the disappearing cookie), and redirects back to the IdP to re-authenticate, creating the loop. A secondary population may be affected by email mismatches between their Entra ID profile and their original TaskFlow account (e.g., email changed in one system but not the other).","reproduction_steps":["Set up TaskFlow v2.3.1 (self-hosted) with Microsoft Entra ID SSO","Create a test user in Entra ID with a plus-addressed email (e.g., testuser+tag@company.com)","Ensure a corresponding account exists in TaskFlow for that email","Attempt to log in as that user via SSO","Observe the infinite 302 redirect loop between TaskFlow and Microsoft"],"environment":"TaskFlow v2.3.1, self-hosted on Ubuntu (likely 22.04), SSO via Microsoft Entra ID (likely OIDC)","severity":"high","impact":"Approximately 30% of the team is completely unable to log into TaskFlow. This is a total access blocker for affected users with no known workaround.","recommended_fix":"Investigate the SSO callback handler in TaskFlow's authentication code. Specifically: (1) Check how the email claim from the OIDC token is parsed and matched against stored user records — look for URL-encoding issues with the '+' character ('+' may be decoded as a space). (2) Check whether the session/cookie is only set after a successful user lookup, and whether a failed lookup silently redirects rather than returning an error. (3) Consider adding a case-insensitive, normalized email comparison that handles plus-addressing. (4) For email-mismatch cases, check whether TaskFlow matches on a stored immutable identifier (like an OIDC 'sub' claim) rather than relying solely on email matching.","proposed_test_case":"Create automated tests for the SSO callback handler that verify: (a) a user with a '+' in their email can complete authentication without looping, (b) the session cookie is correctly set and persists after the callback, (c) an email claim that doesn't match any stored user results in a clear error page rather than a redirect loop, and (d) email matching is consistent regardless of URL-encoding of special characters.","information_gaps":["Exact authentication protocol (OIDC vs SAML) — reporter needs to confirm with DevOps","Server-side logs during the redirect loop — reporter needs to involve their server admin","Whether the non-plus-addressed affected users have an email mismatch between Entra ID and TaskFlow","Exact Ubuntu version (reported as ~22.04 but unconfirmed)"]}

---
