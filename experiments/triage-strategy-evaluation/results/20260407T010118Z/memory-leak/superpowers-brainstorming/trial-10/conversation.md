# Conversation: memory-leak x superpowers-brainstorming

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana data — that consistent memory climb is a strong signal of a leak.

To help narrow down the cause, could you share which version of TaskFlow you're running and how it's deployed?

- **A)** Docker container (official image)
- **B)** Docker container (custom image)
- **C)** Bare metal / VM with Node.js directly
- **D)** Kubernetes / orchestrated containers
- **E)** Something else

Also, which TaskFlow version (or approximate install/upgrade date) if you have it handy?

---

### reporter

We're running it on a VM (option C) — Ubuntu 22.04, 8GB RAM. We upgraded to TaskFlow v2.3 about a week ago, and now that you mention it, that's right around when this problem started. Before the upgrade we were on v2.2 and I don't recall having this issue at all.

---

### triage-agent

Thanks — the timing with the v2.3 upgrade is a really strong clue. This looks like a memory leak regression introduced in that release.

One more thing that would help a developer zero in on the source: does the memory growth look steady throughout the day, or does it correlate with usage patterns?

- **A)** Steady climb regardless of activity (grows even on weekends/off-hours)
- **B)** Climbs faster during heavy usage, slower when quiet
- **C)** Seems to jump in steps rather than a smooth climb
- **D)** Not sure / haven't looked that closely

This would help distinguish between a per-request leak, a background process leak, or something like a connection pool that's not releasing properly.

---

### reporter

Good question — it's definitely option B. The memory climbs faster during business hours when everyone's actively using it, and on weekends when only a handful of people are on, it still climbs but much more slowly. So it really does seem tied to how much the system is being used. The more requests hitting the server, the faster memory grows.

---

### triage-agent

[RESOLVED] {"title":"Memory leak regression in v2.3: per-request memory growth causes server slowdown requiring daily restart","problem":"After upgrading from TaskFlow v2.2 to v2.3, the server exhibits a per-request memory leak. Memory climbs from ~500MB at startup to 4GB+ over the course of a business day, causing page loads to exceed 10 seconds and API timeouts. The leak rate correlates directly with request volume — faster during peak hours, slower on weekends — confirming it is triggered by request handling rather than a background process.","root_cause_hypothesis":"A code change in v2.3 introduced a per-request memory leak, most likely objects (such as request contexts, database connections, cached query results, or event listeners) being allocated per request but not released for garbage collection. Common culprits include closures capturing request-scoped data in a module-level cache, growing arrays/maps that are never pruned, or event listeners registered per request without removal.","reproduction_steps":["Deploy TaskFlow v2.3 on a VM or container with memory monitoring enabled","Run a sustained load test simulating typical user activity (~200 concurrent users or equivalent request rate)","Monitor Node.js heap usage over 1-2 hours","Observe steady memory growth proportional to request count with no plateau","Optionally repeat with v2.2 to confirm the leak is absent in the prior release"],"environment":"Ubuntu 22.04 VM, 8GB RAM, TaskFlow v2.3, Node.js (version unspecified), ~200 active users","severity":"high","impact":"All 200 active users experience progressively degrading performance throughout the day, culminating in timeouts. The team is forced to restart the server daily as a workaround, causing brief downtime.","recommended_fix":"Diff the codebase between v2.2 and v2.3, focusing on request-handling middleware, database query paths, and any new caching or event-listener logic. Take a heap snapshot under load using Node.js --inspect and Chrome DevTools (or a tool like clinic.js) to identify which objects are accumulating. Look specifically for: (1) growing Maps/Sets/arrays at module scope, (2) event listeners added per request without cleanup, (3) connection pool or session objects not being released. Once the leaking allocation is identified, ensure proper cleanup on request completion.","proposed_test_case":"Add a load/soak test that sends N thousand requests over a sustained period and asserts that heap usage after a full GC does not exceed a threshold (e.g., startup memory + 20%). This test should run against both v2.2 (baseline, should pass) and v2.3 (should fail before fix, pass after).","information_gaps":["Exact Node.js version running on the server","Whether any TaskFlow plugins or custom middleware are installed that might contribute","Which specific endpoints or features are used most heavily (could help narrow the leak source, but the v2.2-v2.3 diff is a more direct path)"]}

---
