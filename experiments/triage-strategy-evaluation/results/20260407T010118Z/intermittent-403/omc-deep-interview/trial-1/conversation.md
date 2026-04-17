# Conversation: intermittent-403 x omc-deep-interview

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s are frustrating. A couple of things that would help us narrow this down:

When you hit the 403, is it the entire dashboard page that fails to load, or does the page partially load and then specific sections or API calls return 403? If you open your browser's developer tools (Network tab) and reproduce the error, can you tell us which specific URL(s) are returning the 403?

---

### reporter

It's the whole page — when it happens, the entire dashboard just shows a 403 Forbidden error. It's not like parts of it load and other parts don't. I haven't tried the developer tools thing, but I can tell you it's kind of like every third time I refresh? I've been just hitting refresh until it works. I thought it might be network issues on my end but my coworker who also just got the analyst role says the same thing is happening to her.

---

### triage-agent

That's really helpful context, thanks. You mentioned your coworker also just got the analyst role — did you also recently get assigned that role, or have you had it for a while? And do you know if anyone with a different role (like admin or project manager) is seeing the same 403 errors? I'm trying to figure out if this is tied to a specific role or permission level.

---

### reporter

Yeah, I just got the analyst role about the same time — maybe two days ago? That's when this started happening actually. As for other roles, I asked my manager who's an admin and he said he's not having any issues. I think it might just be us analyst people but I'm not 100% sure. My coworker and I are the only ones I've talked to directly about it.

---

### triage-agent

That timing is really interesting — the 403s starting right when the analyst role was assigned is a strong clue.

One more thing that would help us zero in on this: you mentioned it's roughly every third refresh. Is that pattern fairly consistent (like it reliably fails on roughly every 2nd or 3rd attempt), or is it more unpredictable than that? And when it does fail, if you immediately hit refresh again, does it usually come back right away?

This will help us figure out whether the issue is a server configuration that's out of sync across our infrastructure versus something else going on with the permission checks.

---

### reporter

Yeah it's pretty consistent actually. Like if I sit there and refresh a few times, it'll work, work, fail, work, work, fail — roughly that pattern. It's not perfectly every third time but it's close enough that I started noticing it. And yeah, when it fails, if I just hit refresh once or twice it comes right back. That's why I've been assuming it's some kind of network glitch, but the fact that it's so regular is kind of weird now that you mention it.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden on dashboard for analyst role users — likely load balancer / server config inconsistency","problem":"Users with the analyst role receive 403 Forbidden errors on roughly every 3rd request to the dashboard. The errors began approximately 2 days ago, coinciding with when the analyst role was assigned to these users. The entire page fails (not partial). Admins are unaffected. At least 2 users are impacted. Refreshing once or twice reliably restores access.","root_cause_hypothesis":"The application is served behind a load balancer distributing requests across approximately 3 backend servers. One of these servers has a stale, missing, or incorrectly configured permission/authorization entry for the analyst role. Requests routed to that server return 403; requests to the other servers succeed. This explains the roughly-every-third-request failure pattern, the immediate recovery on refresh, and the role-specific scope.","reproduction_steps":["Log in as a user with the analyst role","Navigate to the dashboard","Refresh the page repeatedly (5-10 times)","Observe that approximately every 3rd request returns a 403 Forbidden error","Confirm the issue does not occur for admin users"],"environment":"Affects users with the analyst role. At least 2 users confirmed affected. Admin role users are not affected. Issue began approximately 2 days ago. No specific browser/OS dependency reported — pattern is consistent across at least 2 different users/machines.","severity":"high","impact":"All users with the analyst role are likely affected. The dashboard is intermittently inaccessible (~33% of requests fail). Users can work around it by refreshing, but this degrades the experience significantly and may cause data loss if the 403 hits during form submissions or other write operations.","recommended_fix":"1. Check how many backend servers/instances are behind the load balancer and compare their authorization/permission configurations for the analyst role. 2. Look for a recent deployment or config change ~2 days ago that may have been applied to only some instances (rolling deploy that partially failed, missed config sync, stale cache). 3. Verify the analyst role exists and has dashboard access permissions in all server instances' auth configuration. 4. If using a permission cache, check TTL and invalidation — one server may have cached a pre-analyst-role permission state.","proposed_test_case":"Send N sequential authenticated requests to the dashboard endpoint as an analyst-role user and assert that all return 200. Optionally pin requests to each backend server individually (via sticky sessions or direct addressing) to identify which server returns 403.","information_gaps":["Exact number and identity of backend servers behind the load balancer","Whether a deployment or configuration change occurred ~2 days ago","Whether other roles besides analyst and admin are affected","Whether the 403 also affects other analyst-accessible pages or only the dashboard"]}

---
