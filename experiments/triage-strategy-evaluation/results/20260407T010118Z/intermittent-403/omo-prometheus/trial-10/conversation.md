# Conversation: intermittent-403 x omo-prometheus

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s affecting multiple people definitely needs attention.

To start narrowing this down: were there any deployments, infrastructure changes, or updates to your authentication/SSO provider around the time this started (~2 days ago)? And when you hit the 403, does refreshing the page immediately work, or do you have to wait/re-login before the dashboard loads again?

---

### reporter

I think there was some kind of update a couple days ago — I remember getting an email about a new role being added to our accounts. I was switched to an 'analyst' role around that time. As for the 403, if I just refresh the page it usually works after a try or two. I don't have to re-login or anything. I kind of assumed it was a network issue on our end but my coworker who also got the analyst role is seeing the same thing so maybe not.

---

### triage-agent

That's a really helpful detail — the timing of the 'analyst' role change lining up with the 403s is a strong signal. A couple of follow-ups on that:

1. What role were you on before you were switched to 'analyst'?
2. When the 403 hits, is it always on the main dashboard page, or does it also happen when you navigate to specific sections (e.g., task lists, reports, settings)? Basically — is it always the same URL that fails, or different pages?

This will help us figure out whether the new role is missing a permission entirely (which would fail consistently) or whether something else is causing the intermittent behavior.

---

### reporter

I think I was on 'viewer' before — or maybe 'editor'? I'm not 100% sure, I'd have to check with my manager who made the switch. As for the 403s, it's not just the dashboard actually. It happens on other pages too — task lists, reports, basically anywhere. When it hits, nothing loads at all, I just get the 403 page. Then I refresh and it works fine. It really does feel random, like maybe 1 out of every 3 times I load a page it fails.

---

### triage-agent

Thanks — the pattern is becoming clearer. Here's our working theory: the new 'analyst' role was recently added, and it looks like the system isn't consistently recognizing its permissions. If this were a straightforward missing permission, you'd get 403 *every* time — the fact that it's roughly 1-in-3 suggests something like a cache or a deployment inconsistency where some servers know about the 'analyst' role's permissions and others don't yet.

To help us confirm: next time you hit a 403, could you open your browser's developer tools (F12 → Network tab), find the failed request, and check two things?
1. **The response body** — does it say anything specific, like "insufficient permissions" or "role not recognized," or is it just a generic 403 page?
2. **Any `X-Server` or `X-Request-ID` header** in the response — this would tell us if different servers are giving different results.

No rush on this — just next time it happens naturally. This will tell us exactly where the inconsistency lives.

---

### reporter

Oh okay, I can try that next time. I'm not super familiar with the developer tools but I'll see if I can find those headers. I did actually just get a 403 a few minutes ago so I'll try to reproduce it. Give me a bit and I'll report back. One thing I can say is that when I've seen the 403 page it's pretty generic — just says "403 Forbidden" with no real details, not like a specific error message about permissions or roles or anything. I'll look for that server header thing next time though.

---

### triage-agent

No rush on the dev tools check — take your time with that.

In the meantime, a quick question that could confirm our theory: you mentioned your coworker who also got the 'analyst' role is seeing the same thing. Do you know if anyone on your team who was **not** switched to the 'analyst' role is also getting these 403s? If the people on other roles (viewer, editor, etc.) are completely fine, that would strongly point to the new role as the culprit.

---

### reporter

Good question — yeah, now that I think about it, the people on my team who didn't get switched to the analyst role haven't mentioned any issues. My manager is still on admin and she says everything's been fine for her. It really does seem like it's just the people who got the new analyst role.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden errors for users assigned the new 'analyst' role","problem":"Users who were recently assigned the newly created 'analyst' role experience intermittent 403 Forbidden errors on all pages (~1 in 3 page loads). Refreshing the page usually resolves it within one or two attempts. No re-login is required. Users on other roles (admin, editor, viewer) are unaffected.","root_cause_hypothesis":"The new 'analyst' role's permissions are not consistently recognized across all server instances or through the permissions caching layer. The intermittent nature (not every request, resolves on refresh) strongly suggests either: (1) a rolling deployment that hasn't fully propagated the analyst role's permission set to all application servers, or (2) a permissions cache that intermittently evicts or fails to resolve the analyst role, falling back to a deny-by-default behavior. A consistently missing permission would produce 403 on every request, not 1-in-3.","reproduction_steps":["Ensure a user account is assigned the 'analyst' role","Navigate to any page in TaskFlow (dashboard, task lists, reports)","Observe that approximately 1 in 3 page loads returns a generic 403 Forbidden page","Refresh the page — it typically loads successfully on the next attempt","Repeat with a user on a different role (admin/editor/viewer) to confirm they are unaffected"],"environment":"Production TaskFlow instance. Issue began approximately 2 days ago, coinciding with the introduction of the 'analyst' role. Multiple users on the same team affected. Specific infrastructure details (number of app servers, load balancer config, caching layer) need to be checked server-side.","severity":"high","impact":"All users assigned the 'analyst' role are affected. Roughly 1 in 3 page loads fail, significantly disrupting their workflow. Workaround exists (refresh the page), but it degrades the user experience and erodes trust. No data loss observed.","recommended_fix":"1. Audit the analyst role's permission configuration across all application server instances — check for inconsistencies in deployment or config propagation. 2. Inspect the permissions/authorization caching layer (e.g., Redis, in-memory cache) for how role-to-permission mappings are stored and evicted — verify the analyst role is cached consistently. 3. Check if the role was added to the database but not to a static permission map or config file that some servers read. 4. Compare the analyst role definition with working roles (viewer, editor, admin) to find structural differences. 5. Review server-side access logs filtered by 403 responses to see if failures correlate with specific server instances (would confirm uneven deployment).","proposed_test_case":"Create an integration test that assigns a user the 'analyst' role and makes 20+ sequential authenticated requests to various endpoints. Assert that all requests return 200 (not 403). Run this test against each application server instance individually to detect per-instance inconsistencies.","information_gaps":["Exact number and topology of application server instances behind the load balancer","Whether the 403 responses correlate with specific server instances (X-Server header — reporter is attempting to capture this)","Exact previous role of affected users (reporter unsure if it was 'viewer' or 'editor')","Whether the analyst role was added via a migration, config file update, or admin UI — and whether that change required a restart or cache flush"]}

---
