# Conversation: memory-leak x structured-triage

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent memory climb pattern is very helpful and strongly suggests a memory leak.

To help us narrow down the source, could you share some details about your server environment?

- What **TaskFlow version** are you running?
- What **OS and runtime** is the server on (e.g., Node.js version, Python version, Docker image)?
- What **database** backend are you using (PostgreSQL, MySQL, SQLite)?
- How much **total RAM** does the server have?

This will help us check whether this matches any known issues for your setup.

---

### reporter

We're running TaskFlow v2.3 — we upgraded from v2.2 about a week ago, which is actually right around when this problem started, now that I think about it. The server is an Ubuntu 22.04 VM with 8GB of RAM. I'm not sure about the exact runtime version off the top of my head, but it's whatever ships with the standard TaskFlow v2.3 install. We're using PostgreSQL for the database.

---

### triage-agent

Thanks for that info — the timing correlation with the v2.3 upgrade is a really important clue.

Do you see any error messages or warnings in the TaskFlow server logs as the slowdown progresses? In particular, anything related to database connections, caching, or background jobs would be very helpful. If you're not sure where the logs are, they're typically at `/var/log/taskflow/` or visible via `journalctl -u taskflow`.

Even a "no errors, just slow" answer is useful — it helps us rule out certain failure modes.

---

### reporter

I checked the logs and there aren't any obvious errors or crashes — no database connection errors or anything like that. It just gradually gets slower. The only thing I noticed is that toward the end of the day, there seem to be a lot of log entries related to the notification system — like WebSocket connections and push notification events. The volume of those log lines seems higher than I'd expect, but I'm not sure if that's normal for v2.3. They added 'improved real-time notifications' in the v2.3 changelog so maybe it's just more verbose logging? No errors though, just a lot of activity there.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in TaskFlow v2.3 causes progressive slowdown requiring daily restart","problem":"After upgrading to TaskFlow v2.3, the server's memory usage climbs steadily from ~500MB at startup to 4GB+ over the course of a day, causing page loads exceeding 10 seconds and API timeouts. The server requires a daily restart to recover. ~200 active users are affected.","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 is likely leaking WebSocket connections or accumulating notification state in memory. The high volume of WebSocket/push notification log entries toward end of day suggests connections are not being properly closed or cleaned up, leading to the observed memory leak.","reproduction_steps":["Deploy TaskFlow v2.3 on a server with PostgreSQL backend","Allow normal usage by multiple users over the course of a full workday","Monitor memory usage via Grafana or similar — expect steady climb from ~500MB","Observe progressive slowdown in page loads and API response times as memory grows"],"environment":"Ubuntu 22.04 VM, 8GB RAM, TaskFlow v2.3 (upgraded from v2.2), PostgreSQL, standard TaskFlow v2.3 runtime","severity":"high","impact":"All ~200 active users experience degraded performance daily, with the application becoming effectively unusable by late afternoon. Requires manual daily restarts as a workaround.","recommended_fix":"Investigate the v2.3 real-time notification system for WebSocket connection leaks. Check whether connections are properly closed on client disconnect, whether notification event listeners are being cleaned up, and whether any in-memory caches (e.g., per-connection notification queues) grow unbounded. Compare the notification subsystem code between v2.2 and v2.3 for changes that could cause accumulation. A heap dump comparison between startup and late-day would confirm the leaking objects.","proposed_test_case":"Write a load test that simulates 200 users connecting and disconnecting WebSocket notification channels over several hours. Assert that memory usage remains within a bounded range (e.g., does not exceed startup + 500MB) and that closed connections are fully garbage collected.","information_gaps":["Exact runtime version (Node.js/Python) — reporter unsure, but unlikely to change the investigation direction","Whether a v2.2 rollback confirms the issue is version-specific (reporter noted timing correlation but hasn't tested rollback)","Heap dump or memory profiler output identifying the specific leaking objects"]}

---
