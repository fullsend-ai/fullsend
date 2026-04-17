# Conversation: memory-leak x superpowers-brainstorming

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent memory climb is a clear signal of a leak.

One question to help narrow this down: is this a recent change in behavior, or has TaskFlow always needed daily restarts?

- **Recent** — it started after a specific upgrade, config change, or around a certain date
- **Gradual** — it's been getting worse over time (e.g., used to last 2 days, now only lasts 1)
- **Always** — it's behaved this way since you deployed it
- **Not sure**

---

### reporter

It's recent — it started about a week ago after we upgraded from v2.2 to v2.3. Before that upgrade, the server ran fine for weeks without needing a restart. The timing lines up exactly with the upgrade.

---

### triage-agent

That's very helpful — the v2.2 → v2.3 upgrade timing makes this almost certainly a regression in that release.

To help a developer zero in on the right subsystem: does the memory growth seem to correlate with **user activity**, or does it climb steadily even during quiet periods (e.g., overnight or weekends if the server stays up)?

- **Correlates with activity** — it grows faster during peak hours and plateaus when fewer people are using it
- **Steady climb regardless** — it grows at roughly the same rate even when nobody is actively using TaskFlow
- **Not sure** — you haven't had a chance to observe it during a quiet period

---

### reporter

It definitely correlates with activity. During business hours when all 200 users are active, the memory climbs rapidly. We left the server running over a weekend once and the memory barely moved compared to a weekday. The growth is clearly tied to how many people are using the system.

---

### triage-agent

[RESOLVED] {"title":"Memory leak regression in v2.3: request-correlated memory growth requires daily restart","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory in proportion to user activity. Memory climbs from ~500MB at startup to 4GB+ over a business day with 200 active users, causing page loads of 10+ seconds and API timeouts by late afternoon. The server must be restarted daily. The leak is dormant during low-activity periods (weekends), confirming it is tied to request handling, not background processes.","root_cause_hypothesis":"A code change in v2.3 introduced a per-request resource leak — most likely objects being added to an in-memory cache, event listener list, or connection pool that are never released. Common patterns: missing cleanup in middleware, unbounded memoization/cache without eviction, event listeners registered per-request but never removed, or database/HTTP connections not being returned to their pool.","reproduction_steps":["Deploy TaskFlow v2.3 with default configuration","Start monitoring memory usage (e.g., process RSS or heap size)","Simulate sustained user activity (200 concurrent users or equivalent load test)","Observe memory climbing steadily over several hours without returning to baseline","Compare with the same test against v2.2 to confirm the regression"],"environment":"Self-hosted TaskFlow v2.3, ~200 active users, upgraded from v2.2 approximately one week ago. Grafana monitoring in place showing consistent day-over-day memory growth pattern.","severity":"high","impact":"All 200 users experience degraded performance (10+ second page loads, API timeouts) by late afternoon every day. Requires manual daily restart as a workaround, risking service disruption and data loss if the restart is missed and the server exhausts memory.","recommended_fix":"1. Diff v2.2 and v2.3, focusing on request-handling code paths: middleware, controllers, caches, connection pools, and event listener registration. 2. Run a heap profiler (or equivalent for the server's runtime) under load against v2.3 to identify which object types are accumulating. 3. Look specifically for: unbounded caches missing eviction policies, event listeners attached per-request without cleanup, connection/resource handles not closed, and closures capturing large scopes in hot paths. 4. Once identified, fix the leak and verify memory stays stable under sustained load.","proposed_test_case":"A soak test that simulates 200 concurrent users over a 4–8 hour period and asserts that server memory (RSS or heap) remains within a bounded range (e.g., does not exceed 2× the startup baseline). This test should run against both v2.2 (as a passing baseline) and the patched v2.3.","information_gaps":["Exact server runtime and framework (Node.js, Python, Java, etc.) — affects which profiling tools to use, but does not change the investigation approach","Whether any v2.3-specific features (new integrations, plugins, config options) are enabled that might narrow the search — developer can check the changelog"]}

---
