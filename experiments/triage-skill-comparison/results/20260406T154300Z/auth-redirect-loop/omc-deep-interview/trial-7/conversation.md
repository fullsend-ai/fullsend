# Conversation: auth-redirect-loop x omc-deep-interview

## Issue

**Login redirect loop after SSO change**

We recently switched our company's SSO provider from Okta to Microsoft Entra ID. Since the switch, some users are getting stuck in an infinite redirect loop when trying to log into TaskFlow. They click login, get redirected to Microsoft, authenticate successfully, get redirected back to TaskFlow, and then immediately get sent back to Microsoft again. Not all users are affected — maybe 30% of our team. The rest can log in fine.

We've already verified that the Entra ID app registration is correct and matches our redirect URIs. The affected users can log into other apps that use the same SSO just fine.

---

## Comments

### triage-agent

Thanks for the detailed report. One thing that would really help narrow this down: have you noticed any pattern among the affected users versus the ones who can log in successfully? For example — were the affected users perhaps already logged into TaskFlow (with active sessions) at the time of the SSO switch, while the unaffected users were not? Or is there a difference in their Entra ID account attributes, like group membership or MFA configuration? Any distinguishing pattern you've spotted would help a lot.

---

### reporter

Good question. I don't think it's related to active sessions — we actually had everyone log out before the switch. Looking at who's affected, I did notice something: a lot of the affected users are people who use plus-addressed emails, like jane+taskflow@company.com. We have a handful of people who do that. Also, a few users who had their email addresses changed or aliased when we set up Entra ID seem to be affected too. The users who log in fine tend to be the ones whose plain email addresses are the same in both systems. I'm not sure if that helps, but that's the pattern I've noticed so far.

---

### triage-agent

[RESOLVED] {"title":"Login redirect loop for users with plus-addressed or aliased emails after Okta-to-Entra-ID SSO migration","problem":"After switching SSO from Okta to Microsoft Entra ID, approximately 30% of users experience an infinite redirect loop: they authenticate successfully at Microsoft but are immediately redirected back to Microsoft upon returning to TaskFlow. The affected users disproportionately have plus-addressed emails (e.g., jane+taskflow@company.com) or had their email addresses changed/aliased during Entra ID setup.","root_cause_hypothesis":"TaskFlow's session establishment logic compares the email claim in the incoming OIDC/SAML token against stored user records using an exact string match. Okta likely normalized or preserved plus-addressed emails in a way that matched TaskFlow's records, but Entra ID either strips the plus-address tag, lowercases differently, or returns the primary alias instead of the plus-addressed variant. When the lookup fails, TaskFlow cannot establish a session and redirects back to the IdP, creating the loop. The same issue affects users whose canonical email changed during the Entra ID migration — the token email no longer matches the stored email.","reproduction_steps":["Configure TaskFlow to use Microsoft Entra ID for SSO","Create or ensure a user account in TaskFlow associated with a plus-addressed email (e.g., testuser+taskflow@company.com)","Ensure the corresponding Entra ID account has the base email (testuser@company.com) or a different alias as its primary","Attempt to log in as that user","Observe the infinite redirect loop between TaskFlow and the Entra ID login page"],"environment":"TaskFlow with SSO configured against Microsoft Entra ID (recently migrated from Okta). Affects users with plus-addressed emails or email aliases that differ between the identity provider and TaskFlow's user records.","severity":"high","impact":"~30% of the team is completely unable to log into TaskFlow. These users are fully blocked with no known workaround. Other SSO-dependent apps work fine for them, so the issue is specific to TaskFlow's token-to-user matching logic.","recommended_fix":"Investigate the authentication callback handler where TaskFlow matches the incoming token's email claim to a stored user record. (1) Log the exact email claim value Entra ID sends vs. what TaskFlow has stored for affected users. (2) Implement email normalization: strip plus-address tags before comparison, apply case-insensitive matching, and consider matching against email aliases. (3) Provide an admin tool or migration script to reconcile user email records with Entra ID identities. (4) If TaskFlow caches the old Okta token format or issuer, ensure token validation accepts the new Entra ID issuer.","proposed_test_case":"Unit test the user-lookup-by-email function with inputs: exact match, plus-addressed variant, different-case variant, and alias email. Verify that all variants resolve to the correct user record. Integration test: complete an SSO login flow with a plus-addressed email and confirm a session is established without redirect looping.","information_gaps":["Exact email claim values Entra ID is sending in tokens for affected users (server-side log inspection needed)","Whether TaskFlow stores emails from the original Okta provisioning or from its own user registration","Whether the app uses OIDC or SAML with Entra ID, which affects how email claims are formatted"],"clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.75,"impact":0.7,"overall_ambiguity":0.19}}

---

