# Conversation: auth-redirect-loop x socratic-refinement

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed write-up — the 30% affected vs 70% unaffected split is really interesting and likely the key to figuring this out. Have you noticed any pattern among the affected users? For instance, are they in a particular group or role in your identity provider, did they have active Okta sessions when the switch happened, or are they using a specific browser? Anything you've noticed that the affected users have in common — even if it seems unrelated — would be really helpful.

---

### reporter

Good question. I've been trying to figure out a pattern myself. The main thing I've noticed is that a lot of the affected users have '+' signs in their email addresses — we use plus addressing for some people, like jane+taskflow@company.com. Also, a few users who don't have the '+' thing are affected too, and those are people whose email addresses changed at some point — like they had one email in Okta and then we set up an alias in Entra ID that maps to a different primary address. Browser doesn't seem to matter, we're seeing it on Chrome, Edge, and Firefox. One weird thing I noticed — sometimes if an affected user opens an incognito window, they can occasionally get in, but it's not consistent.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop caused by email claim mismatch after Okta-to-Entra ID migration","problem":"After migrating SSO from Okta to Microsoft Entra ID, approximately 30% of users are stuck in an infinite authentication redirect loop. Users authenticate successfully with Entra ID but TaskFlow fails to establish a session, redirecting them back to the IdP repeatedly.","root_cause_hypothesis":"TaskFlow matches authenticated users by comparing the email claim from the IdP against stored user records. Two populations are affected: (1) Users with '+' in their email addresses — the plus sign is likely being corrupted during the OAuth/SAML callback URL processing ('+' decoded as a space, or double-encoded as '%2B'), causing the email lookup to fail. (2) Users whose primary email changed between Okta and Entra ID — the email claim returned by Entra ID doesn't match the email stored in TaskFlow's user table from the Okta era. In both cases, the failed lookup means no session is created, and the auth middleware redirects back to login, producing the loop.","reproduction_steps":["1. Set up a user account in TaskFlow with a plus-addressed email (e.g., jane+taskflow@company.com)","2. Configure SSO with Microsoft Entra ID","3. Attempt to log in with that user","4. Observe: user authenticates successfully at Microsoft but gets redirected back to TaskFlow login in a loop","5. Alternatively: set up a user whose Entra ID primary email differs from their stored TaskFlow email (e.g., old Okta email was jdoe@company.com, Entra ID returns jane.doe@company.com)"],"environment":"TaskFlow with Microsoft Entra ID SSO (recently migrated from Okta). Affects all browsers (Chrome, Edge, Firefox). Incognito windows occasionally bypass the issue, suggesting stale session cookies from Okta may be a contributing factor.","severity":"high","impact":"~30% of the team cannot log into TaskFlow at all. This is a complete blocker for those users with no reliable workaround.","recommended_fix":"Investigate two areas: (1) In the SSO callback handler, check how the email claim is extracted and decoded — ensure '+' characters survive URL decoding intact (check for application/x-www-form-urlencoded vs proper handling of the '+' character in query parameters or POST bodies). (2) In the user lookup logic, consider matching on a stable identifier beyond just email (e.g., the IdP subject/oid claim), or implement a case-insensitive email lookup that also checks known aliases. For the migration specifically, either update stored emails to match Entra ID primary addresses, or add a mapping table for email aliases. Additionally, clear or invalidate any residual Okta session cookies that may be interfering.","proposed_test_case":"Create test users with plus-addressed emails (user+tag@domain.com) and with mismatched primary emails between IdP claim and stored record. Verify that the SSO callback correctly resolves both to valid user sessions without redirect loops. Also verify that the '+' character is preserved through the full callback URL decode path.","information_gaps":["Which protocol is used — SAML 2.0 or OIDC/OAuth 2.0 — would clarify exactly where the encoding issue occurs","Whether TaskFlow stores the IdP subject identifier (sub/oid) in addition to email, which would enable a more robust fix","The exact non-plus-addressed users affected beyond the email-change cases — there may be a third subpopulation"]}

---

