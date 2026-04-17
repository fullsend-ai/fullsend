# Conversation: intermittent-403 x omc-deep-interview

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this. To help us narrow down what's going on — have you noticed any pattern to when the 403 appears? For example, does it happen more after you've been logged in for a while, or right after logging in? Does it occur on specific pages within the dashboard, or when performing a particular action (like loading a project or switching views)? Also, did anything change around the time this started — a new deployment, SSO/auth configuration update, or infrastructure change that you're aware of?

---

### reporter

Honestly I don't think it's related to how long I've been logged in — it happens right away sometimes too. It seems to affect everything, not just one specific page. Like I'll click on the dashboard and get a 403, then refresh and it works fine. It feels almost like a network issue to me, like maybe 1 out of 3 times it fails? I'm not sure what changed on your end, but I did get a new role assigned recently — I was moved to the 'analyst' role around the same time this started. Not sure if that's related. My coworkers who also got the analyst role seem to be having the same problem, now that I think about it.

---

### triage-agent

That's a really helpful detail about the analyst role change — thanks for connecting those dots. Quick follow-up: do you know if anyone on your team who was *not* moved to the analyst role is also seeing these 403 errors? If the problem is exclusive to people with the new analyst role, that would significantly narrow down where to look.

---

### reporter

Hmm, good question. I don't think so actually — I asked around and the people who are still on the editor or admin roles say everything's working fine for them. It really does seem like it's just those of us who got switched to the analyst role. I didn't think much of it at first because my permissions were supposed to stay the same, just under a different role name, but yeah it does seem like it's only us.

---

### triage-agent

That really does point toward the analyst role — thanks for checking with your team. One thing that's puzzling me: if it were a straightforward missing permission on the analyst role, you'd expect it to fail *every* time, not just roughly 1 in 3. The fact that it's intermittent is an important clue.

A couple of things that could explain the inconsistency: do you happen to know if TaskFlow runs behind a load balancer or has multiple servers? (You might see this in the URL, or your ops team might know.) Alternatively, when you get the 403 and then refresh successfully, does anything look different — like a brief re-login redirect, a different URL, or any flash of a login screen before the page loads? I'm trying to figure out whether the intermittency is server-side (different servers disagree about your permissions) or session-related (your permissions are sometimes evaluated differently within the same server).

---

### reporter

I'm not 100% sure about the infrastructure setup, but I think we do run multiple servers behind a load balancer — I've heard our ops team mention something like that before. I don't know the specifics though. When I get the 403 and refresh, it just loads normally — no login redirect or anything like that. It's like the page just decides to work the second time. Nothing looks different when it does load, it's the same URL and everything. So yeah, it does kind of feel like maybe one of the servers doesn't like my role or something? That would explain the 1-in-3 thing I guess.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden for users with 'analyst' role — likely inconsistent role/permission config across load-balanced servers","problem":"Users recently assigned the 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 requests to any dashboard page. Refreshing the page typically succeeds. The issue affects all analyst-role users and no other roles. No login redirect or session anomaly occurs on the successful retry.","root_cause_hypothesis":"The application runs behind a load balancer with multiple backend servers. When the 'analyst' role was created or configured, the role-to-permission mapping was not consistently deployed or propagated to all servers. Requests routed to server(s) missing the analyst role's permissions return 403; requests routed to correctly configured server(s) succeed. This explains both the intermittency and the role-specific scope.","reproduction_steps":["Log in as a user assigned the 'analyst' role","Navigate to the TaskFlow dashboard","Refresh the page repeatedly (roughly 1 in 3 attempts should return 403)","Confirm that users with 'editor' or 'admin' roles do not experience the error"],"environment":"Multi-server deployment behind a load balancer. Exact server count and configuration unknown. The 'analyst' role was introduced/assigned a few days ago.","severity":"high","impact":"All users assigned the 'analyst' role are intermittently locked out of the entire dashboard. No other roles are affected. The only workaround is refreshing the page, which is unreliable and disruptive.","recommended_fix":"1. Compare the role/permission configuration (RBAC tables, policy files, or authorization config) across all backend servers to identify which server(s) are missing the analyst role definition or its permission grants. 2. Deploy the correct analyst role permissions to all servers. 3. If permissions are cached, flush the authorization cache on all instances. 4. Review the deployment/config-sync process to ensure future role changes propagate atomically to all servers.","proposed_test_case":"Send N sequential authenticated requests (e.g., 20) as an analyst-role user to the dashboard endpoint, pinning each request to a specific backend server (via direct IP or sticky header). Verify that every server returns 200. Repeat after any config deployment to confirm consistency.","information_gaps":["Exact number of backend servers and which specific instance(s) are misconfigured","How the analyst role was provisioned — via database migration, config file, admin UI — and whether the deployment process covers all servers","Whether the application caches role/permission lookups and what the cache TTL or invalidation mechanism is"]}

---
