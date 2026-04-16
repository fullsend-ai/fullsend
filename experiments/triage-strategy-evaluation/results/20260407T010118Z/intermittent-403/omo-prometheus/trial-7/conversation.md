# Conversation: intermittent-403 x omo-prometheus

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s affecting multiple team members definitely sounds like something changed on the backend side.

To start narrowing this down: can you tell me if there were any deployments, infrastructure changes, or auth/SSO configuration updates around the time this started (~2 days ago)? Also, when you hit the 403, is it a TaskFlow error page or a raw server response? If you can grab the full response body and any relevant response headers (especially anything auth-related like `WWW-Authenticate`), that would be very helpful.

---

### reporter

I'm not sure about deployments — I'm not on the infrastructure team, but I did hear something about a new role being added recently. I was actually just assigned the 'analyst' role around that time. When I get the 403, it looks like a standard server error page, not a custom TaskFlow one. I haven't looked at the response headers honestly, I just hit refresh and it usually works on the second or third try. I kind of assumed it was network issues on our end.

---

### triage-agent

That new 'analyst' role is a really useful detail — the timing lines up well with when the 403s started.

Quick question: do the other team members who are also seeing these 403 errors also have the 'analyst' role? And do you know if anyone on your team who does *not* have the 'analyst' role is experiencing the same issue?

---

### reporter

Yeah, now that you mention it, I think the others who complained are also analysts. My manager has an admin role and she said everything's been fine for her. I hadn't connected those dots until you asked — I just figured it was happening to everyone randomly.

---

### triage-agent

Great catch on the analyst connection — that's almost certainly the thread to pull on.

I want to understand the intermittent pattern better, because if it were a simple missing permission, you'd get 403 *every* time. Can you notice any pattern to when it fails vs. succeeds? Specifically:

- Does it tend to fail more right after you first log in, or after you've been idle for a while and come back?
- When you hit refresh and it works, does it then stay working for a stretch, or can it fail again immediately on the next page load?

This will help us figure out whether this is a caching issue, a deployment inconsistency, or something else entirely.

---

### reporter

It doesn't seem to be related to logging in or being idle — it can fail pretty much any time. Like I'll load the dashboard, it works, I click to another page, 403, I refresh, it works again. There's no real pattern I can see except that it feels like maybe 1 out of every 3 times it fails? It's not consistent like 'every third click' but roughly that proportion. It can fail again immediately after a refresh works, or it might work several times in a row. Honestly I just keep refreshing until it goes through.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden for users with 'analyst' role — likely deployment inconsistency across backend instances","problem":"Users assigned the newly-added 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 page loads across the entire application. The errors are not tied to specific pages, login state, or session age. Refreshing typically succeeds on the 2nd or 3rd attempt. Users with the 'admin' role are unaffected. The issue began roughly when the 'analyst' role was introduced.","root_cause_hypothesis":"A load balancer is distributing requests across multiple backend instances (likely 3, given the ~1/3 failure rate), and one instance does not have the 'analyst' role properly registered in its authorization/permission configuration. This could be caused by a rolling deployment that didn't fully propagate, a stale config/cache on one instance, or the role definition existing in a config file or database cache that one instance hasn't picked up. When requests hit the misconfigured instance, it doesn't recognize the 'analyst' role and returns 403.","reproduction_steps":["Log in as a user with the 'analyst' role","Navigate to the dashboard or any page","Refresh or navigate repeatedly — approximately 1 in 3 requests will return a 403 Forbidden","Note: the 403 is a standard server error page, not a custom TaskFlow error page"],"environment":"Production environment. Multiple analyst-role users affected across the same team. Admin-role users are not affected. Issue started approximately 2 days ago, coinciding with the introduction of the 'analyst' role.","severity":"high","impact":"All users with the 'analyst' role are affected. The application is partially unusable — users can work around it by refreshing, but ~33% of requests fail, significantly degrading productivity and trust. No data loss, but core functionality is unreliable.","recommended_fix":"1. Check how many backend instances are running and compare their authorization/role configurations — look for one that is missing the 'analyst' role definition. 2. Check if a recent deployment rolled out unevenly or if one instance is running an older version. 3. Verify the role config source (database, config file, env var) is consistent across all instances. 4. Restart or redeploy the misconfigured instance. 5. If roles are cached, check cache invalidation — one instance may have a stale cache from before the 'analyst' role was added.","proposed_test_case":"Send 20+ sequential authenticated requests as an analyst-role user to the dashboard endpoint and assert all return 200. Optionally, pin requests to each backend instance individually (via direct IP or header) and verify each instance returns 200 for analyst-role users.","information_gaps":["Exact number of backend instances behind the load balancer","Whether the 'analyst' role was added via config file, database migration, or environment variable","Exact deployment timeline and whether a rolling deployment was used","Server-side access logs correlating 403s to specific instance IPs"]}

---
