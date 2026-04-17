# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up. The fact that only ~30% of users are affected is a strong clue. Have you been able to identify any pattern among the affected users versus the ones who can log in fine? For example:

- Were the affected users already logged into TaskFlow (with active sessions) at the time of the SSO switch, while the others weren't?
- Do the affected users belong to different groups, roles, or organizational units in Entra ID?
- Does clearing cookies/browser data for the TaskFlow domain resolve it for any of them?

Any commonality you've noticed — even a guess — would help narrow this down.

---

### reporter

Good questions. I've been trying to figure out the pattern myself. It doesn't seem to be about whether they were logged in during the switch — we had everyone log out before we cut over. And it's not a group or role thing in Entra ID either, I checked that.

What I did notice is that the affected users tend to be the ones who use plus-addressed emails — like jane+taskflow@company.com — or people whose emails changed at some point (we aliased some emails when we moved to Entra). The users who log in fine are the ones with straightforward email addresses that haven't changed.

As for clearing cookies — it's hit or miss. Sometimes clearing all cookies and trying again quickly works, and a couple people have had luck using incognito windows. But it's not a reliable fix, it just sometimes works.

---

### triage-agent

That email pattern is a really strong lead — plus-addressed and aliased emails being the common factor points toward an identity-matching issue on the TaskFlow side.

To help pin this down: are you able to check TaskFlow's server-side logs during a failed login attempt from one of the affected users? Specifically, I'm curious whether:

1. TaskFlow receives the callback from Entra ID and processes the token (suggesting the app gets the identity but can't match it to a user record), or
2. TaskFlow never seems to process the callback at all (suggesting something is rejecting it earlier, at the session/cookie layer)

If you can share any relevant log output — even partial — that would help confirm whether this is an email claim mismatch in user lookup or something else entirely.

---

### reporter

I did pull up the logs after one of the affected users tried to log in. It looks like TaskFlow does receive the callback from Entra — I can see the token being processed. But then it seems to immediately redirect back to the login page. What's weird is I also noticed in the browser dev tools that there's a Set-Cookie header in the redirect response coming back from Entra, but the cookie doesn't seem to actually stick in the browser. So it's like the app is trying to set the session but the browser isn't keeping it, and then TaskFlow sees no session and kicks them back to login. I'm not sure if that's a cookie issue or an email matching issue or both, honestly.

---

### triage-agent

Thanks — the cookie detail is really helpful. Two quick things that would crack this open:

1. In the server logs where you see the token being processed, does TaskFlow actually find and match a user record (e.g., a log line like "authenticated user jane+taskflow@company.com"), or does it seem to fail the lookup silently before redirecting?

2. Can you grab the actual `Set-Cookie` header from the browser dev tools (Network tab → the callback response)? Specifically the attributes — `Domain`, `Path`, `SameSite`, `Secure`, `HttpOnly`. I'm wondering if the cookie attributes might be causing the browser to reject it outright, which would be a separate issue from the email matching and could explain why incognito is inconsistent.

This will tell us whether we're dealing with one problem or two stacked on top of each other.

---

### reporter

I went back and checked the logs more carefully. It does look like TaskFlow processes the token and tries to match a user, but I'm not 100% sure if it's finding the right record — the logs aren't super verbose on that part. For one of the plus-address users, I didn't see an obvious 'user not found' error, but it just redirects right after.

As for the Set-Cookie header, I had one of our affected users pull it up in Chrome dev tools. The cookie has SameSite=Strict on it. I'm not a cookie expert, but could that be a problem since the redirect is coming back from Microsoft's domain to our TaskFlow domain? The other attributes looked normal — Secure, HttpOnly, path was '/'. I'm starting to wonder if this is why incognito sometimes works — maybe different cookie handling there?

---

### triage-agent

[RESOLVED] {"title":"SameSite=Strict session cookie causes infinite redirect loop on SSO callback from Entra ID","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Entra ID, get redirected back to TaskFlow, but the session cookie (set with SameSite=Strict) is not persisted by the browser on the cross-site redirect, so TaskFlow sees no session and redirects back to the IdP. Users with plus-addressed or aliased emails are disproportionately affected, suggesting a possible secondary user-lookup issue.","root_cause_hypothesis":"The session cookie is set with SameSite=Strict. In an OAuth/OIDC flow, the callback is a cross-site navigation from the IdP (login.microsoftonline.com) to the app. Browsers do not attach SameSite=Strict cookies on cross-site navigations, so the session cookie is silently dropped. TaskFlow then detects no active session and redirects to the IdP again, creating the loop. This likely worked with Okta due to different redirect timing or cookie handling but broke with Entra ID. The correlation with plus-addressed/aliased emails may indicate a secondary issue where the email claim in the Entra ID token doesn't exactly match the stored user record in TaskFlow (e.g., case normalization or plus-address stripping), but the cookie issue alone is sufficient to cause the loop regardless of email format.","reproduction_steps":["Configure TaskFlow SSO to use Microsoft Entra ID as the identity provider","Ensure TaskFlow's session cookie is set with SameSite=Strict","Log in as a user (especially one with a plus-addressed email like user+tag@company.com)","Authenticate successfully at Entra ID","Observe the redirect back to TaskFlow — the Set-Cookie header is present in the response but the cookie is not persisted by the browser","TaskFlow detects no session and redirects back to Entra ID, creating an infinite loop"],"environment":"TaskFlow with Microsoft Entra ID SSO (migrated from Okta). Session cookie attributes: SameSite=Strict, Secure, HttpOnly, Path=/. Observed in Chrome. Some users have plus-addressed or aliased email addresses.","severity":"critical","impact":"~30% of users are completely unable to log into TaskFlow. No reliable workaround exists — clearing cookies and incognito mode work intermittently at best. This is a complete authentication blocker for affected users.","recommended_fix":"1. **Primary fix:** Change the session cookie's SameSite attribute from `Strict` to `Lax`. `SameSite=Lax` allows cookies on top-level cross-site navigations (like OAuth callbacks) while still protecting against CSRF on sub-requests. This is the standard setting for applications using OAuth/OIDC flows. 2. **Secondary investigation:** Once the cookie issue is fixed, verify that user lookup on the Entra ID callback correctly matches plus-addressed and aliased emails. Check whether the email claim from the Entra ID token is being compared case-insensitively and whether plus-address suffixes are handled consistently between the stored user record and the incoming claim. Add verbose logging around user lookup during SSO callback to surface any silent mismatches. 3. **Optional hardening:** Consider adding a log warning when a session cookie is set but not received on the next request, to make this class of issue easier to diagnose in the future.","proposed_test_case":"Write an integration test that simulates the full OAuth callback flow with a cross-site redirect: set the session cookie in the callback response, perform the redirect as the browser would (respecting SameSite rules), and assert that the session is established and the user is not redirected back to the login page. Test with SameSite=Lax (should pass) and SameSite=Strict (should fail or be flagged). Additionally, write a unit test for user lookup that verifies matching works correctly for plus-addressed emails (user+tag@domain.com) and aliased emails.","information_gaps":["Whether the email claim mismatch is a real secondary issue or merely a correlation masked by the cookie problem — this can only be confirmed after the SameSite fix is deployed","Server-side logs are not verbose enough to confirm whether user lookup succeeds or fails silently — adding logging is recommended","Whether the SameSite=Strict setting was intentional or a framework default that was never adjusted for the SSO flow","Why incognito mode works intermittently — possibly related to stale cookies from the old Okta integration interfering in normal sessions"],"clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.75,"impact":0.85,"overall_ambiguity":0.15}}

---
