# Conversation: intermittent-403 x omo-prometheus

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s affecting multiple people definitely sounds like something changed on the backend rather than a permissions issue on your end.

To start narrowing this down: were there any deployments, infrastructure changes, or updates to your authentication/SSO setup in the days before this started? And when you hit the 403, is it a TaskFlow error page or a raw server response? If you can, grab the full response body and any request/response headers from your browser's Network tab next time it happens — that will tell us exactly what's rejecting the request.

---

### reporter

I think there was some kind of deployment a couple days ago — I heard something about new roles being added or permissions changes, but I'm not super involved in the admin side of things. I was recently given a new 'analyst' role around the same time, not sure if that's related.

When I get the 403 it looks like a pretty standard error page, nothing fancy. I'll try to grab the headers from the network tab next time it happens, but honestly it's kind of hard to catch since it's so random. I just hit refresh and it usually works the second or third try.

---

### triage-agent

That 'analyst' role assignment is a really important detail — the timing lines up perfectly with when the 403s started.

Two things I need to narrow this down: Do the teammates who are also seeing the 403s also have the 'analyst' role? And when the 403 hits, does it happen on any dashboard page equally, or are there specific pages or actions (e.g., viewing reports, editing tasks) where it seems to happen more often?

---

### reporter

Hmm, good question. I think at least a couple of them were also given the analyst role recently — we're on the same team and we all got switched over around the same time. I'm not sure about everyone though. I can ask around.

As for which pages, it seems pretty random honestly. It's not like one specific page is broken — sometimes the main dashboard loads fine, sometimes reports, sometimes neither. When it does fail it fails on whatever I'm trying to load. Like I said, I just refresh and it usually comes back.

---

### triage-agent

Thanks — the pattern is getting clearer. Here's my working theory: the deployment that added the 'analyst' role may not have fully propagated across all your backend instances, or there's a permission cache that intermittently serves stale authorization data. That would explain why it fails randomly and works on refresh (you hit a different instance or the cache refreshes).

To confirm this, can you check two things?

1. **Does anyone on your team who was NOT given the 'analyst' role also experience the 403s?** That's the key distinguisher — if it's exclusively analyst-role users, we know exactly where to look.

2. **Next time you hit a 403, try opening a different browser or an incognito window and loading the same page.** If it loads fine in the new session at the same moment, that points to a session/cache issue rather than a deployment inconsistency.

Either answer will significantly narrow the fix direction.

---

### reporter

I'll ask around about #1. Off the top of my head, I don't think anyone who kept their old roles (like editor or viewer) has mentioned having this problem, but I'm not 100% sure. It does seem like it's the people who got the analyst role.

As for #2, I just tried that actually — opened an incognito window and hit the dashboard a few times. It still happened in incognito too, so I don't think it's a browser cache thing. It's weird though, it's almost like every third request fails or something. I figured it was maybe a network issue on our end but now I'm not so sure.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden errors for users with new 'analyst' role — likely deployment inconsistency across backend instances","problem":"Users assigned the newly introduced 'analyst' role are experiencing intermittent 403 Forbidden errors across all dashboard pages. The errors appear on roughly every third request and resolve on refresh. Multiple team members who received the analyst role are affected. Users who retained their previous roles (editor, viewer) do not appear to be affected.","root_cause_hypothesis":"A recent deployment added the 'analyst' role but did not propagate the role's permission grants consistently across all backend instances behind the load balancer. One (or more) instances lacks the correct authorization rules for the analyst role, causing 403s when the load balancer routes to that instance. The roughly 1-in-3 failure rate suggests one of three instances is misconfigured.","reproduction_steps":["Assign a user the 'analyst' role in TaskFlow","Log in as that user and navigate to the dashboard","Refresh the page repeatedly (5-10 times)","Observe that approximately every third request returns a 403 Forbidden","Note that refreshing again typically succeeds (request hits a different backend instance)"],"environment":"TaskFlow dashboard, post-deployment that introduced the 'analyst' role (approximately 2 days before report). Load-balanced backend with likely 3 instances. Affects multiple users on the same team who were batch-assigned the analyst role.","severity":"high","impact":"All users with the 'analyst' role are intermittently locked out of the dashboard. Multiple team members affected. Workaround exists (refresh the page) but degrades user experience and erodes trust. No data loss reported.","recommended_fix":"1. Compare authorization/permission configuration across all backend instances — identify which instance(s) lack the analyst role grants. 2. Check the deployment pipeline for the analyst role rollout — determine why it didn't propagate to all instances (failed rolling update, missed migration, config cache not invalidated). 3. Re-deploy or hot-fix the misconfigured instance(s) to include analyst role permissions. 4. Verify by sending requests directly to each instance (bypassing LB) as an analyst-role user. 5. Consider adding a health check that validates role configuration consistency across instances.","proposed_test_case":"Pin requests to each backend instance individually (via direct addressing or sticky sessions) and authenticate as an analyst-role user. Verify that all instances return 200 for dashboard, reports, and task pages. Additionally, add an integration test that assigns the analyst role to a test user and confirms consistent 200 responses across N sequential requests (to exercise load balancer rotation).","information_gaps":["Exact number of backend instances and which specific instance(s) are misconfigured (requires server-side investigation)","Whether any non-analyst-role users are also affected (reporter believes not, but not 100% confirmed — server logs can verify)","Exact deployment mechanism that caused the inconsistency (rolling update failure, config management issue, etc.)"]}

---
