# Conversation: intermittent-403 x omo-prometheus

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s affecting multiple people definitely sounds like something changed on the backend rather than a permissions issue on your end.

To help narrow this down: can you tell me if anything changed in your environment around the time this started? Specifically:

- Was there a deployment, infrastructure change, or config update roughly 2 days ago?
- Is the 403 happening on the main dashboard page itself (`/dashboard`), or on specific sub-pages or API calls within the dashboard?

If you're not sure about deployments, even knowing the exact date/time you first saw it would help us correlate with deploy logs.

---

### reporter

I'm not sure about the deployment details — I'm not on the ops side of things. But I do know that I was recently given a new 'analyst' role, that was around the same time, maybe a couple days ago? Not sure if that's related.

As for where the 403 happens, it seems like it's the whole dashboard — not a specific page. Like sometimes I'll load `/dashboard` and get a 403, then I refresh and it works fine. It happens on other pages too, not just the dashboard. It's kind of random, almost like every third time it fails? I assumed it was network issues but it's been going on for two days now so that seems unlikely.

---

### triage-agent

Thanks for that detail — the timing of the new 'analyst' role assignment is very interesting and likely related.

I want to dig into the pattern you're seeing. You mentioned it fails roughly every third request. Can you check something for me?

When you get a 403, open your browser's developer tools (Network tab) and look at the failing request:
1. Is the 403 coming from the **page load itself**, or from an **API call** (e.g., something like `/api/...`) that the page makes after loading?
2. In the response body or headers of the 403, is there any message — something like "insufficient permissions," "token expired," or a specific error code?

This will help us determine whether the issue is in the authentication layer (session/token) or the authorization layer (role mapping), and the intermittent pattern may tell us whether it's a caching or load-balancing issue.

---

### reporter

OK I just tried it with the dev tools open. The 403 is coming from the page load itself — like the actual page request returns a 403, not an API call after the page loads. I refreshed a few times and caught one. The response body just says something like "Forbidden - you do not have permission to access this resource." I didn't see a specific error code beyond the 403 status. The response headers didn't have anything that jumped out to me but I'm not really sure what to look for there. I did notice that when it works vs when it doesn't, the responses seem to come back at slightly different speeds, if that helps? Not sure if that means anything.

---

### triage-agent

Thanks — the fact that it's the page load itself returning 403 (not an API call) and the different response speeds are both very helpful clues. The speed difference strongly suggests your requests are being routed to different backend servers, and at least one of them is rejecting you.

Here's my working theory: the new 'analyst' role you were assigned isn't being handled correctly by all backend instances — some accept it, some don't recognize it and return 403. The ~1-in-3 failure rate would match if one out of three servers has a stale or misconfigured role mapping.

To confirm this: **are the other team members who are also seeing 403s also on the 'analyst' role?** And do you know if anyone on your team who does *not* have the 'analyst' role is experiencing the same issue? This will tell us whether it's the role itself that's the problem or something broader.

---

### reporter

Good question. Now that I think about it, yeah — the other people on my team who mentioned having the same problem were also recently given the 'analyst' role. I think there are a couple of people on the team who are still 'editors' and I haven't heard them complain about it. So it does seem like it might be an analyst role thing? That's kind of concerning since we were all just moved to that role.

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden for users with 'analyst' role — likely stale role config on subset of backend instances","problem":"Users assigned the new 'analyst' role receive intermittent 403 Forbidden errors on page loads across all routes (not limited to a specific page or API call). Approximately 1 in 3 requests fail. The issue began roughly 2 days ago, coinciding with the 'analyst' role being assigned to multiple team members. Users with the 'editor' role are unaffected.","root_cause_hypothesis":"The 'analyst' role was recently introduced or assigned, but not all backend server instances behind the load balancer have the updated role/authorization configuration. Requests routed to instances with stale config reject 'analyst' users with 403, while instances with current config serve them normally. The ~1-in-3 failure rate suggests one out of three backend instances is misconfigured or has a stale authorization cache.","reproduction_steps":["Log in as a user with the 'analyst' role","Navigate to any page (e.g., /dashboard)","Refresh repeatedly — approximately 1 in 3 page loads will return 403 Forbidden","Note: users with 'editor' role do not experience this issue"],"environment":"Production environment, multiple backend instances behind a load balancer. Issue affects all users with the 'analyst' role. No specific browser or OS dependency identified — the 403 is server-side.","severity":"high","impact":"All users recently assigned the 'analyst' role are affected, experiencing intermittent inability to access any page in the application. No reliable workaround exists beyond repeatedly refreshing. This blocks normal work for the affected user group.","recommended_fix":"1. Check authorization/role configuration across ALL backend instances — look for the 'analyst' role definition and verify it is present and identical on every instance. 2. If using a role cache, force invalidation or restart on all instances. 3. Verify the deployment that introduced or updated the 'analyst' role was rolled out to all instances (check for a partial or stuck deployment). 4. Consider whether a rolling restart or redeployment resolves the issue immediately.","proposed_test_case":"Send N sequential authenticated requests as an 'analyst' user and verify all return 200. Ideally pin requests to each backend instance individually (via direct instance addressing or sticky headers) and confirm each instance accepts the 'analyst' role. Add an integration test that verifies all defined roles are authorized for their expected routes.","information_gaps":["Exact number of backend instances and their deployment state","Whether the 'analyst' role was added via a recent code change, config change, or admin UI action","Exact deploy or config change timestamp to correlate with the onset of the issue"]}

---
