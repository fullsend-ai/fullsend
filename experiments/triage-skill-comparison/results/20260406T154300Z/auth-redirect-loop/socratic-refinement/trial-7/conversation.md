# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up — the 30/70 split is a really interesting clue and probably the fastest path to figuring this out.

Have you noticed any pattern among the affected users that distinguishes them from the ones who can log in fine? For example: Were the affected users already logged into TaskFlow (with active sessions from the old Okta setup) when you made the switch, while the unaffected ones weren't? Or is there a difference in their roles, group memberships, or how their accounts are set up in Entra ID?

Also — if any of the affected users try logging in from a fresh incognito/private browser window, does the loop still happen?

---

### reporter

Good questions! So the pattern I've noticed is that the affected users tend to be ones whose email addresses have a '+' in them — like jane+taskflow@company.com — we use plus addressing for some folks. There are also a few users who originally signed up for TaskFlow with a different email and then we aliased their new company email in Entra ID later. The users who work fine are the ones with straightforward email addresses that match exactly between the old Okta setup and Entra.

As for incognito — yeah actually, a couple of affected users told me that incognito sometimes works, weirdly enough. Not every time, but sometimes they can get in that way. Clearing all their cookies and trying again quickly also seems to work occasionally. It's inconsistent though.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop after Okta→Entra ID migration for users with plus-addressed or aliased emails","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. Authentication at the Entra ID side succeeds, but TaskFlow fails to establish a session and redirects back to the IdP. Affected users are those with '+' in their email addresses (plus-addressing) or those whose Entra ID email differs from the email they originally registered with in TaskFlow.","root_cause_hypothesis":"TaskFlow's SSO callback handler matches the email claim from the IdP response against its stored user records. Entra ID likely normalizes or encodes the '+' character in email claims differently than Okta did (e.g., URL-encoding '+' as '%2B', or stripping the plus-addressed portion per RFC 5233). When the lookup fails to find a matching user, TaskFlow cannot create a session and falls back to re-initiating the auth flow, causing the loop. For aliased users, Entra ID may return the new aliased email while TaskFlow's user table still holds the original signup email. The intermittent success with incognito/cookie-clearing suggests stale Okta session cookies also interfere — TaskFlow may attempt to validate an old Okta session token, fail, and trigger the redirect before even processing the new Entra ID response.","reproduction_steps":["1. Set up a user account in TaskFlow with a plus-addressed email (e.g., user+tag@company.com)","2. Configure Entra ID as the SSO provider with correct redirect URIs","3. Attempt to log in as that user","4. Observe: Entra ID authentication succeeds, user is redirected back to TaskFlow, then immediately redirected back to Entra ID in a loop","5. Alternatively: create a user with email A, then alias email B in Entra ID — same loop occurs when Entra ID returns email B in the claim"],"environment":"TaskFlow with SSO, recently migrated from Okta to Microsoft Entra ID. Affects multiple browsers. Incognito mode with cleared cookies occasionally allows login to succeed.","severity":"high","impact":"~30% of the team is locked out of TaskFlow entirely. These users cannot reliably log in even with workarounds (incognito is inconsistent). This blocks their daily work in the task management system.","recommended_fix":"1. **Email matching normalization:** In the SSO callback handler, normalize both the incoming email claim and the stored user email before comparison — lowercase, decode URL-encoded characters, and optionally strip plus-addressed tags for matching purposes. 2. **Stale session cleanup:** On the SSO callback endpoint, clear any pre-existing session cookies before processing the new IdP response to prevent stale Okta tokens from interfering. 3. **Fallback matching:** If exact email match fails, attempt a secondary lookup by subject/nameID claim or by domain-stripped username to handle aliased accounts. 4. **Logging:** Add debug logging to the SSO callback to capture the exact email claim received vs. what was looked up in the database, to confirm the mismatch.","proposed_test_case":"Create integration tests for the SSO callback handler that: (a) send an IdP response with a plus-addressed email and verify the user is matched and session is created; (b) send an IdP response where the email differs from the stored email but matches an alias; (c) send an IdP response when a stale session cookie from a different IdP is present and verify the old cookie is cleared and login succeeds.","information_gaps":["Exact OIDC/SAML claim name TaskFlow uses for email matching (email, preferred_username, or nameID)","Whether TaskFlow's user table has a dedicated SSO identity column or relies solely on email matching","Server-side logs from the SSO callback showing the exact failure point in the redirect cycle"]}

---

