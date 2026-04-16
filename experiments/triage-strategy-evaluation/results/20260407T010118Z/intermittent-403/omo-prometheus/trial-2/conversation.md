# Conversation: intermittent-403 x omo-prometheus

## Issue

**Getting random 403 Forbidden errors on the dashboard**

I keep getting 403 errors when I try to access the TaskFlow dashboard. It's not every time — sometimes it loads fine, sometimes I get a 403 page. It seems totally random. My permissions haven't changed and other people on my team say they're seeing it too. Started happening a couple days ago.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent 403s are frustrating, especially when they started suddenly.

To start narrowing this down: when you get the 403, is it on the initial page load of the dashboard, or does it happen on specific actions within the dashboard (e.g., loading a project, fetching tasks)? And can you check your browser's Network tab — is the 403 coming from a specific API endpoint, or is it the page itself returning 403?

Also, you mentioned this started a couple days ago — do you know if there was a deployment, configuration change, or anything else that coincided with when it started?

---

### reporter

It's the whole page that returns 403 — I don't even get to the dashboard when it happens. I just hit refresh and sometimes it works on the next try. I haven't really dug into the Network tab, sorry.

As for timing, I think there was some kind of update or deployment around when it started? I'm not totally sure, but I was recently given a new role — 'analyst' — and it started around the same time. Could be a coincidence though. I assumed it was network issues since it's so random.

---

### triage-agent

That's a really important detail about the new 'analyst' role — that timing is unlikely to be a coincidence.

Can you check with the teammates who are also seeing the 403s: do they also have the 'analyst' role? And on your end, when you get a 403 and then refresh successfully, does anything visually change about your session — for example, are you prompted to log in again, or does the dashboard just load normally on retry?

This will help me determine whether the issue is tied to how the 'analyst' role's permissions are being evaluated versus a session or caching problem.

---

### reporter

Good question — I just asked around and yeah, the ones who are also getting the 403s are the ones who were also given the analyst role recently. My colleague Sarah who's still on the editor role says she hasn't had any issues at all. So that does seem connected.

As for the refresh thing, no, I don't get prompted to log in again or anything. When I hit refresh and it works, the dashboard just loads normally like nothing was wrong. It's weird — it's almost like flipping a coin each time I load the page.

---

### triage-agent

Thanks — that confirms the 'analyst' role is the common thread here, which is very helpful.

I have a working theory I'd like to test: the intermittent nature suggests your requests may be hitting different backend servers, and the 'analyst' role's permissions may not be configured consistently across all of them (e.g., a deployment that didn't fully roll out).

Can you try something quick? When you get a 403, try rapid-refreshing 5–10 times and roughly note how many succeed vs. fail. Is it close to 50/50, or more like 1-in-5 failures? And does it ever fail several times in a row, or does it tend to alternate success/failure?

This pattern will help distinguish between a load-balancer routing issue (where you'd see a consistent ratio like 50/50 or 1-in-3) versus a caching or timing issue (where you'd see clusters of failures followed by clusters of successes).

---

### reporter

Yeah I just tried that actually. Out of about 10 refreshes, I'd say it failed maybe 3 or 4 times. So it's not 50/50, more like every third try or so fails. And it didn't really cluster — it felt pretty evenly spread out, like fail-success-success-fail-success-success-fail kind of pattern. That does sound like the load balancer thing you're describing. Is it possible the analyst role wasn't set up right on one of the servers?

---

### triage-agent

[RESOLVED] {"title":"Intermittent 403 Forbidden on dashboard for users with 'analyst' role due to inconsistent backend permission configuration","problem":"Users assigned the recently-introduced 'analyst' role receive 403 Forbidden errors on approximately 1 in 3 dashboard page loads. The errors are intermittent and evenly distributed (not clustered), consistent with load-balancer routing across multiple backend servers where one or more servers lack the correct permission mapping for the analyst role. Users with the 'editor' role are completely unaffected.","root_cause_hypothesis":"A recent deployment that introduced or configured the 'analyst' role did not fully roll out across all backend servers behind the load balancer. Approximately 1 out of 3 backend instances is missing the analyst role in its permission/RBAC configuration, causing requests routed to that instance to return 403. This explains the ~30% failure rate with even distribution and no session/auth disruption on successful retries.","reproduction_steps":["Log in as a user with the 'analyst' role","Navigate to the TaskFlow dashboard","Refresh the page 10+ times","Observe that approximately 1 in 3 loads returns a 403 Forbidden error","Note that no re-authentication is required — subsequent refreshes may succeed immediately"],"environment":"Production environment, affects all users with the 'analyst' role. Started approximately 2 days ago coinciding with a deployment and the introduction of the analyst role. Multiple team members confirmed affected. Users on the 'editor' role are unaffected.","severity":"high","impact":"All users assigned the 'analyst' role are affected. They experience intermittent inability to access the dashboard (~30% of requests fail), forcing repeated page refreshes. This degrades productivity and trust in the platform for this user group. No workaround exists other than retrying.","recommended_fix":"1. Check how many backend server instances are running and compare their RBAC/permission configurations for the 'analyst' role. Look for instances that were missed during the recent deployment. 2. Verify the deployment rollout status — check if any instances are running an older version or config that predates the analyst role definition. 3. Once the misconfigured instance(s) are identified, redeploy or update the permission configuration. 4. Consider adding the RBAC/role configuration to a shared config store or database rather than per-instance config to prevent future inconsistencies during rolling deployments.","proposed_test_case":"After fix: Authenticate as an analyst-role user and make 50 consecutive requests to the dashboard endpoint. Assert that all 50 return 200. Additionally, add an integration test that verifies all defined roles (including 'analyst') are recognized and authorized for their expected endpoints across all backend instances.","information_gaps":["Exact number of backend server instances behind the load balancer","Whether the analyst role is defined in per-instance config vs. a shared database","Exact deployment tool and rollout strategy used (rolling update, blue-green, etc.)","Whether server access logs confirm the 403s correlate with a specific instance"]}

---
