# Conversation: memory-leak x omc-deep-interview

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily pattern is really helpful.

To help us narrow down where the memory is being consumed: what does your deployment look like? Specifically, are you running TaskFlow as a single process or with multiple workers, and is the memory growth happening in the TaskFlow application process itself or could it be the database (e.g., PostgreSQL) or a cache layer (e.g., Redis) that's growing? If you can check `top` or `docker stats` during the high-memory period, knowing which process owns the 4GB would point us in the right direction.

---

### reporter

We're running TaskFlow as a single process on an Ubuntu 22.04 VM with 8GB RAM. I checked `top` during the high-memory period and it's definitely the TaskFlow application process itself that's consuming all the memory — not PostgreSQL or Redis. PostgreSQL stays steady around 300-400MB and Redis is minimal. The TaskFlow process is the one ballooning up to 3-4GB by end of day. We don't use multiple workers, just the default single-process setup.

---

### triage-agent

That's really useful — knowing it's isolated to the TaskFlow process itself rules out a lot of possibilities.

You mentioned this has been happening for the past week. Did anything change around when it started — a TaskFlow version upgrade, a configuration change, a new integration or webhook, or a significant increase in usage? Or has this been a gradual onset that you only recently noticed? Knowing whether there was a clear "before and after" would help us zero in on the likely cause.

---

### reporter

Yes, actually — we upgraded from TaskFlow v2.2 to v2.3 about a week ago, and that's exactly when this started. Before the upgrade, memory usage was stable all day, never went above 1GB. The timing lines up perfectly. I remember the v2.3 changelog mentioned 'improved real-time notifications' as one of the new features, among other things. No other changes on our end — same config, same number of users, no new integrations.

---

### triage-agent

The timing with the v2.3 upgrade is a strong clue, and the 'improved real-time notifications' feature stands out as a likely culprit — real-time features can leak memory if connections or event listeners aren't cleaned up properly.

A couple of things that would help us confirm this: does the memory growth seem to correlate with how many users are actively connected (e.g., does it grow faster during peak hours vs. quieter periods)? And does TaskFlow v2.3 expose any way to disable or configure the real-time notifications feature — for instance, a config flag or environment variable? If so, it would be very informative to run a day with that feature disabled to see if memory stays stable.

---

### reporter

Good thinking on the correlation with active users — yes, the memory definitely grows faster during business hours when most of our 200 users are active. We have about 25 requests per user per hour during peak times, and the memory climbs noticeably faster then compared to evenings or weekends when only a handful of people are on. The pattern on Grafana clearly shows a steeper slope during 9am-5pm.

As for disabling real-time notifications, I haven't looked into that yet but I can check the config. That's a great idea — I'll try running a day with it disabled if there's a flag for it and report back. Do you know offhand what the config option might be called, or should I just grep through the config file?

---

### triage-agent

[RESOLVED] {"title":"Memory leak in TaskFlow process since v2.3 upgrade — likely real-time notifications connection/listener leak","problem":"The TaskFlow application process leaks memory throughout the day, climbing from ~500MB at startup to 4GB+ by end of day on a single-process deployment with 200 active users. This causes progressive slowdowns (10+ second page loads) and API timeouts, requiring daily restarts. The issue began exactly when upgrading from v2.2 to v2.3.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature is likely not cleaning up WebSocket connections, event listeners, or in-memory subscription state when users disconnect or navigate away. Memory growth correlates with active user count (steeper during business hours), consistent with per-connection or per-session objects accumulating without being garbage collected.","reproduction_steps":["Deploy TaskFlow v2.3 as a single process (default configuration)","Allow normal user activity from ~200 users over a business day","Monitor the TaskFlow process memory via `top` or `docker stats`","Observe memory climbing steadily, with a steeper slope during peak usage hours (9am-5pm)","Memory will reach 3-4GB by end of day, causing page load times >10 seconds and API timeouts"],"environment":"Ubuntu 22.04 VM, 8GB RAM, TaskFlow v2.3, single-process deployment (no workers), PostgreSQL and Redis running on same host but not contributing to the leak. ~200 active users, ~25 requests/user/hour at peak.","severity":"high","impact":"All 200 users experience progressively degraded performance throughout the day, with the application becoming effectively unusable by late afternoon. Operations team must restart the server daily. No data loss reported but availability is significantly impaired.","recommended_fix":"Investigate the real-time notifications code path introduced in v2.3. Look for: (1) WebSocket or SSE connections not being closed/removed on client disconnect, (2) event listeners or pub/sub subscriptions accumulating without cleanup, (3) in-memory caches of notification state growing without eviction or TTL. Compare the connection lifecycle management with v2.2 to identify what changed. As an immediate mitigation, check if real-time notifications can be disabled via configuration flag to confirm the hypothesis and restore stable operation while a fix is developed.","proposed_test_case":"Deploy TaskFlow v2.3 in a test environment, simulate 50+ concurrent users connecting and disconnecting repeatedly over several hours, and assert that memory usage remains bounded (e.g., stays below 1.5GB). Additionally, verify that after all users disconnect, memory returns to near-baseline levels within a reasonable GC window.","information_gaps":["Exact configuration flag (if any) to disable real-time notifications in v2.3","Whether the leak is in WebSocket connections, SSE streams, or an in-memory data structure","Whether v2.3.x patch releases have already addressed this"]}

---
