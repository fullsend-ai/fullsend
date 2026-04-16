# Conversation: intermittent-403 x socratic-refinement

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s are tricky, so let's try to narrow down what's going on together.

When it does work versus when it doesn't, can you walk me through what you're typically doing? For example: are you loading the dashboard fresh after logging in, refreshing an already-open page, navigating to it from somewhere else in the app, or coming back to it after being idle for a while? I'm curious whether there's a pattern in *how* you're reaching the dashboard when the 403 appears versus when it loads fine.

---

### reporter

Honestly, I don't think it matters how I get there. I've seen it happen when I first log in, when I refresh the page, when I navigate from another section — it's pretty random. Sometimes I'll just hit refresh a couple times and it works again. I figured it was maybe a network issue or something on our end. The weird thing is it seems like roughly every third attempt fails, but that might just be coincidence. My coworker who also just got the new analyst role says she's seeing the same thing.

---

### triage-agent

That's a really helpful detail — the fact that your coworker with the same analyst role is experiencing it too is a strong clue.

You mentioned she "just got" the new analyst role — can you tell me more about that? When were you and your coworker assigned the analyst role, and was it around the same time the 403 errors started appearing? Also, do you know if anyone on your team who has a *different* role (say, admin or manager) is seeing these errors too, or is it just folks with the analyst role?

---

### reporter

Yeah, we both got the analyst role right around the time this started — maybe two days ago? I think it was part of some update they rolled out. And now that you mention it, I asked a couple people on the team who have the regular editor or admin roles and they said they haven't had any issues. So it does seem like it might be related to the analyst role specifically. I hadn't really connected those dots until you asked.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden on dashboard for users with new analyst role — likely inconsistent role configuration across app instances","problem":"Users assigned the recently-introduced 'analyst' role are intermittently receiving 403 Forbidden errors when accessing the TaskFlow dashboard. The errors occur regardless of navigation method (fresh login, refresh, internal navigation). Users with other roles (editor, admin) are unaffected. The issue began approximately two days ago, coinciding with the rollout of the analyst role.","root_cause_hypothesis":"The application runs behind a load balancer with approximately three backend instances (inferred from the 'roughly every third attempt fails' pattern). When the analyst role was deployed, not all instances received the updated authorization/permissions configuration. Requests routed to the misconfigured instance(s) fail with 403 because they do not recognize the analyst role's dashboard access grant. Refreshing works when the load balancer routes the retry to a correctly-configured instance.","reproduction_steps":["Assign a user the 'analyst' role","Attempt to load the TaskFlow dashboard","Refresh the page repeatedly — approximately 1 in 3 attempts should return 403 Forbidden","Compare with a user who has the 'editor' or 'admin' role — those users should never see 403s"],"environment":"Production environment, multiple team members affected. Analyst role was rolled out approximately two days ago. Load-balanced deployment suspected (multiple app or auth server instances).","severity":"high","impact":"All users with the analyst role are intermittently locked out of the dashboard. This is a blocking issue for the newly-rolled-out role, affecting team productivity and undermining trust in the new role assignment.","recommended_fix":"1. Check all application/auth server instances for consistent RBAC or permissions configuration — look for an instance missing the analyst role's dashboard access grant. 2. Verify the deployment mechanism for role/permission changes ensures all instances are updated atomically. 3. If using a permissions cache, check TTL and invalidation across nodes. 4. As an immediate mitigation, restart or redeploy all instances to force config synchronization.","proposed_test_case":"Send N sequential dashboard requests (e.g., 30) authenticated as an analyst-role user and assert that all return 200. Run this against each backend instance individually (bypassing the load balancer) to confirm parity.","information_gaps":["Exact number of app/auth server instances behind the load balancer","Whether the analyst role was newly created or modified from an existing role","Deployment mechanism used for the role rollout (config change, DB migration, feature flag, etc.)"]}

---
