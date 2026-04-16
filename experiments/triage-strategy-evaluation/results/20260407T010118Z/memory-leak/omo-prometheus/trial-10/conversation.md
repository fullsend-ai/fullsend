# Conversation: memory-leak x omo-prometheus

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana data — the consistent day-over-day pattern is very helpful.

To narrow this down: when memory climbs to 4GB+, which process is consuming it? Specifically, is it the TaskFlow application server process (e.g., Node, Java, Python — whichever runtime you're on), the database, a cache like Redis, or something else on the host? If you can run something like `top` or `docker stats` when things are slow and share which process is using the most memory, that would point us in the right direction.

Also, which TaskFlow version are you running, and what's the runtime/stack (e.g., Node.js 18, Python 3.11, etc.)?

---

### reporter

It's the TaskFlow application server process itself that's eating all the memory. When I run `top` during the slowdown, the TaskFlow process is sitting at 3-4GB while everything else (the database, Redis) looks normal. We're running TaskFlow v2.3 — we actually just upgraded from v2.2 about a week ago, which is right around when this started happening. I should have mentioned that earlier, sorry. As for the runtime, I believe it's Go but I'd have to double-check — I'm mainly the sysadmin, not the developer on the project.

---

### triage-agent

That's a really important detail — the timing of the v2.3 upgrade lining up with when this started is a strong signal. Two things I want to nail down:

1. **Can you confirm causation?** Have you tried rolling back to v2.2, even briefly, to see if the memory growth stops? That would confirm this is a v2.3 regression versus a coincidence.

2. **Does the memory growth correlate with user activity or just time?** For example, does it climb faster during peak usage hours (when your 200 users are most active) versus overnight or on weekends when the server is idle? If the server sits idle overnight without a restart, does memory still climb, or does it plateau?

---

### reporter

1. No, I haven't tried rolling back to v2.2 yet — I was hesitant to do that because the upgrade included some features our team was asking for, and I wasn't sure if there would be database migration issues going backward. But I can try it if you think it's worth the risk.

2. It definitely correlates with user activity. The memory climbs fastest during business hours when everyone is actively using it. Over the weekend when only a handful of people are on, it climbs much more slowly — we actually made it through the whole weekend without needing a restart once. And overnight, the growth pretty much plateaus. So it's clearly tied to usage volume, not just the passage of time.

---

### triage-agent

This is shaping up clearly as a v2.3 regression — the timing and the usage-correlated growth both point that way.

Here's my working theory: v2.3 introduced a per-request resource leak — something like a goroutine that never terminates, connections that aren't closed, or an in-memory cache that grows without eviction. Each user action allocates something that never gets freed.

To narrow down *where* in v2.3 the leak is: you mentioned the upgrade included features your team was asking for. **Which new v2.3 features is your team actively using?** For example, are there new dashboard views, search capabilities, integrations, or workflow features that your users adopted right away? If the leak is in a new code path, the feature your team hammered on day one would be the prime suspect.

Also — since it's a Go application, if you're comfortable with it, you could hit the `/debug/pprof/heap` endpoint (if enabled) or run `go tool pprof` against the process when memory is high. That would give us a direct stack trace showing exactly what's holding the memory. No pressure if that's outside your comfort zone — we can work without it.

---

### reporter

Looking at the v2.3 changelog, the main thing our team was excited about was the 'improved real-time notifications' feature — that's actually the primary reason we upgraded. Our users have been actively using the push notifications for task updates, comments, things like that. There were a couple of other minor things in the release but the real-time notifications were the big one everyone jumped on right away.

As for the pprof thing, I'm not super familiar with Go tooling but I can try to hit that endpoint if you can point me to exactly what to run. I'm comfortable with the command line, just not with Go-specific debugging tools.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3: per-request resource leak likely in real-time notifications feature causes OOM-level growth under normal usage","problem":"TaskFlow v2.3 application server process leaks memory proportional to user activity. Memory grows from ~500MB at startup to 4GB+ over a business day with ~200 active users, causing 10+ second page loads and API timeouts. Requires daily restart. The issue began immediately after upgrading from v2.2 to v2.3.","root_cause_hypothesis":"The v2.3 'real-time notifications' feature (push notifications for task updates, comments, etc.) is leaking a per-request resource — most likely goroutines, WebSocket connections, or notification subscription objects that are allocated on each user action but never released. In a Go application, the most common pattern would be goroutines spawned per notification event or per subscriber connection that are never cancelled/closed, or an in-memory subscriber registry that grows without eviction when clients disconnect.","reproduction_steps":["Deploy TaskFlow v2.3 on a clean instance with real-time notifications enabled","Simulate sustained user activity (task creation, comments, status changes) with multiple concurrent users","Monitor the TaskFlow server process memory via `top`, `docker stats`, or Grafana","Observe memory climbing steadily with no plateau, proportional to request volume","Compare against v2.2 under identical load to confirm the regression"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2 ~1 week ago), Go runtime, ~200 active users, with Redis and database on the same host (both behaving normally)","severity":"high","impact":"All 200 active users experience progressively degrading performance throughout the workday, culminating in 10+ second page loads and API timeouts by late afternoon. Requires daily manual restart. No data loss reported, but productivity impact is significant.","recommended_fix":"1. Diff the v2.3 real-time notifications code path against v2.2 — look for goroutine spawns without corresponding cancellation, WebSocket/SSE connections without close handlers, or in-memory subscriber maps that grow without cleanup on disconnect. 2. Run `go tool pprof` against `/debug/pprof/heap` and `/debug/pprof/goroutine` on a live instance after several hours of usage to get direct evidence of what's accumulating. 3. Check whether notification subscriber cleanup fires on client disconnect/timeout. 4. Verify any new caches introduced in v2.3 have eviction policies.","proposed_test_case":"Load test: simulate 50+ concurrent users generating notification-triggering actions (task updates, comments) for 2+ hours. Assert that server memory stays within a bounded range (e.g., <1.5GB) and that goroutine count does not grow monotonically. Additionally, simulate client disconnects during active notification streams and verify that server-side resources (goroutines, subscriptions) are cleaned up within a reasonable timeout.","information_gaps":["Exact pprof heap/goroutine profile has not been captured yet (reporter willing to try; developer can also do this directly)","Rollback to v2.2 has not been attempted to definitively confirm regression (reporter hesitant due to migration concerns)","Exact Go version not confirmed (reporter believes it's Go but is the sysadmin, not the developer)","Whether /debug/pprof endpoints are enabled in the current deployment is unknown"]}

---
