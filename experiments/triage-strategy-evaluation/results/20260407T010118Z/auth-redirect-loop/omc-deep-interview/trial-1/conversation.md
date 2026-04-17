# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the clear write-up. To help narrow this down: have you noticed any pattern that distinguishes the affected ~30% from the users who can log in fine? For example — are they in a specific group or role in Entra ID, using a particular browser, on a different OS, or did they have existing active sessions in TaskFlow from before the SSO switch? Also, does clearing cookies and browser cache for your TaskFlow domain resolve it for any of the affected users?

---

### reporter

Good question. I haven't noticed a browser or OS pattern — we have people on Chrome, Edge, and Firefox all affected. I did notice that the affected users tend to be ones who use plus-addressing in their email (like jane+taskflow@company.com), and also a few users whose emails were set up as aliases when we migrated to Entra. The users who work fine seem to have straightforward email addresses that match exactly what they had in Okta.

As for clearing cookies — yeah, a couple people reported that clearing cookies and trying again quickly sometimes works, or using an incognito window. But it's not consistent, it seems to break again after a bit.

---

### triage-agent

That plus-addressing and alias pattern is a really helpful clue. To confirm the theory forming here: are you able to check TaskFlow's authentication logs during one of these redirect loops? Specifically, I'm curious whether the email (or identity claim) that Entra ID sends back to TaskFlow matches what's stored in TaskFlow's user database for an affected user. For example, if jane+taskflow@company.com is how she's registered in TaskFlow, does Entra ID return that exact address — or does it return just jane@company.com (or vice versa)? If you don't have easy access to auth logs, comparing an affected user's email in TaskFlow's user table against their UPN or primary email in Entra ID would be just as useful.

---

### reporter

I don't have direct access to TaskFlow's auth logs — I'm more on the IT admin side than the dev side. But I did compare a couple of affected users in Entra ID vs what's in TaskFlow. For one user who uses plus addressing, her TaskFlow account is under jane+taskflow@company.com but I think Entra ID is sending back just jane@company.com as the primary email. So yeah, there's a mismatch there. For the aliased users it's a similar situation — their Entra primary email is different from what TaskFlow has stored.

What I'm not sure about is why clearing cookies sometimes helps temporarily if it's just an email mismatch issue. Like you'd think it would either work or not work, right? But some users say incognito mode works sometimes too, which is weird.

---

### triage-agent

That's a great observation — if it were purely an email mismatch, you'd expect it to consistently fail rather than sometimes work in incognito. That makes me think there might be two things going on.

Could you try something specific with one of the affected users? Have them open an incognito window and log in. If it works, have them stay logged in and note roughly how long it takes before the issue returns — does it break on the next page load, after some idle time, or only after they close and reopen the browser? I'm trying to figure out whether TaskFlow is actually letting them in briefly (meaning it *can* match them somehow) and then losing that match on a session refresh or token renewal, versus the incognito success being inconsistent from the start.

---

### reporter

I can try that. From what I've heard anecdotally from a couple users, when incognito works it seems to let them in and they can use TaskFlow for that session. But if they close the incognito window and open a new one, it's hit or miss again — sometimes it works, sometimes they're right back in the loop. It doesn't seem like it breaks mid-session though, more like the initial login redirect is what either works or doesn't.

One thing I forgot to mention — one of my more tech-savvy users said she opened the browser dev tools during the loop and saw that the server was trying to set a cookie on the redirect back from Microsoft, but she said the cookie wasn't actually sticking. I don't know enough about that to say more but maybe that's related to why incognito is inconsistent?

---

### triage-agent

[RESOLVED] {"title":"Infinite login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra-ID SSO migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop during login. They authenticate successfully with Entra ID but are immediately redirected back to the IdP upon returning to TaskFlow. Affected users are predominantly those with plus-addressed emails (e.g. jane+taskflow@company.com) or email aliases that differ from their Entra ID primary email. The issue is intermittent in incognito mode and clearing cookies sometimes provides temporary relief, but the loop returns on subsequent login attempts.","root_cause_hypothesis":"Two interacting defects: (1) **Email claim mismatch**: Entra ID returns the user's primary email (e.g. jane@company.com) as the identity claim, but TaskFlow's user table stores the plus-addressed or aliased variant (e.g. jane+taskflow@company.com). TaskFlow's auth callback cannot match the incoming claim to a local user, so it fails silently and restarts the auth flow. (2) **Session cookie not persisting**: The auth callback attempts to set a session cookie on redirect, but it is not sticking in the browser — likely due to a SameSite=Strict or SameSite=Lax cookie attribute conflicting with the cross-origin redirect from Entra ID, or a Secure flag issue if the dev/staging environment uses HTTP. This explains why incognito sometimes works (no pre-existing cookies competing) and why the issue is at login time rather than mid-session.","reproduction_steps":["Create or identify a TaskFlow user account registered with a plus-addressed email (e.g. testuser+taskflow@company.com)","Ensure the corresponding Entra ID account's primary email / UPN is the base address (testuser@company.com)","Attempt to log into TaskFlow via SSO","Observe the redirect loop: TaskFlow → Entra ID → successful auth → redirect back to TaskFlow → immediate redirect back to Entra ID","Open browser dev tools Network tab and note: (a) the email claim value in the OIDC callback response, and (b) whether the Set-Cookie header from TaskFlow's callback response is actually stored by the browser"],"environment":"TaskFlow web application, recently migrated from Okta to Microsoft Entra ID for SSO. Multiple browsers affected (Chrome, Edge, Firefox). Issue is independent of OS. Entra ID app registration and redirect URIs have been verified as correct.","severity":"high","impact":"~30% of the organization cannot reliably log into TaskFlow. No consistent workaround exists. Incognito/cookie-clearing provides only intermittent, temporary relief. Affected users are blocked from using the application.","recommended_fix":"Investigate two areas: (1) **Auth callback user lookup** — check how TaskFlow resolves the incoming identity claim (email/UPN) to a local user. Add case-insensitive matching and normalize plus-addressed emails so that jane+taskflow@company.com matches jane@company.com. Alternatively, configure Entra ID to send a custom claim that includes the full plus-addressed email, or switch the matching key to a stable identifier like the Entra Object ID / OIDC `sub` claim rather than email. (2) **Session cookie attributes** — inspect the Set-Cookie header on TaskFlow's auth callback response. Ensure SameSite=Lax (not Strict) so the cookie is sent on the top-level redirect from Entra ID. Verify the Secure flag matches the protocol in use and the Domain/Path attributes are correct. Check for conflicts with any existing cookies from the old Okta integration.","proposed_test_case":"Test with a user whose TaskFlow account email differs from their Entra ID primary email (plus-addressing and alias variants). Verify: (a) login completes without redirect loop, (b) the session cookie is correctly set and persists across page loads, (c) token renewal does not break the session, (d) users with exact-match emails continue to work as before.","information_gaps":["Exact OIDC claim TaskFlow uses for user matching (email, preferred_username, sub, etc.) — requires reading the auth code","Exact cookie attributes (SameSite, Secure, Domain, Path) set by TaskFlow's auth callback — requires inspecting server config or code","Whether TaskFlow's auth error handling silently restarts the auth flow on user-not-found vs. showing an error page","Whether any old Okta session cookies are still present and conflicting with the new Entra ID flow"]}

---
