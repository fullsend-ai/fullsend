# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent memory climb is very telling.

I'd like to understand more about how your team uses TaskFlow throughout the day. Can you walk me through what a typical day of activity looks like on your instance? For example, are there particular features your 200 users rely on heavily (real-time collaboration, file attachments, recurring task automation, integrations with other tools), or does the usage pattern shift during the day in a way that might correlate with the slowdown? I'm trying to get a sense of what the server is *doing* as the memory climbs.

---

### reporter

Our usage is pretty steady throughout the day — most of our 200 users are active from around 9am to 5pm. They're mainly creating and updating tasks, leaving comments, and using the real-time notifications to stay on top of changes. We don't use file attachments heavily, and we have a couple of integrations (Slack and email notifications) but nothing unusual. There's no big spike at any particular time — it's a fairly even load across the workday, maybe 25 or so API requests per user per hour on average. The memory just climbs linearly regardless of what people are doing. One thing I should mention — this only started after we upgraded to v2.3 about a week ago. We were on v2.2 for months before that with no issues like this.

---

### triage-agent

That's a really helpful detail about the v2.3 upgrade — that's almost certainly the trigger. I'd like to follow that thread a bit.

Can you tell me more about what changed in v2.3 for your deployment? For instance, did the upgrade involve any configuration changes, database migrations, or changes to how your Slack/email integrations connect? And regarding the real-time notifications your team relies on — do your users tend to keep TaskFlow open in a browser tab throughout the day, or do they open and close it frequently? I'm curious whether the memory growth might correlate with the number of persistent connections (like WebSockets) accumulating over the course of the day.

---

### reporter

The upgrade to v2.3 was pretty straightforward — we followed the standard upgrade docs, ran the database migration script, and it all went smoothly. We didn't change any configuration, and the Slack and email integrations carried over without any changes on our end. The v2.3 changelog mentioned 'improved real-time notifications' as one of the new features, which caught my eye since that's something our team uses a lot.

As for how people use it — most users keep TaskFlow open in a browser tab all day. They rely on the real-time notifications to know when tasks are updated or when someone comments on something they're following. But I'm not sure the memory issue is about persistent connections specifically, because I've checked and the number of active WebSocket connections stays fairly stable throughout the day — it roughly matches our active user count. The memory just keeps climbing even though the connection count stays flat. It's like something is accumulating on the server side with every request, not with every new connection.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification system causes linear memory growth (~3.5GB/day) requiring daily restarts","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server exhibits a consistent memory leak. Memory climbs linearly from ~500MB at startup to 4GB+ over a workday under normal load (200 users, ~5000 req/hr), causing progressive slowdown with 10+ second page loads and API timeouts by late afternoon. The server requires daily restarts to remain functional.","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 is likely accumulating per-request state on the server side without proper cleanup. Since WebSocket connection count remains stable while memory grows linearly with request volume, the leak is not in connection handling but in request-scoped processing — most likely notification event objects, subscription listener registrations, or in-memory notification history/queues that are appended to on each API request but never released or bounded.","reproduction_steps":["Deploy TaskFlow v2.3 with default configuration","Connect ~200 concurrent users (or simulate equivalent load of ~5000 API requests/hour) with persistent browser sessions using real-time notifications","Monitor server memory usage over several hours","Observe linear memory growth of approximately 400-500MB per hour under sustained load"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2 via standard upgrade path with database migration), ~200 active users, Slack and email notification integrations enabled, users maintain persistent browser sessions throughout the workday","severity":"high","impact":"All 200 users on this instance experience progressively degrading performance throughout the workday, culminating in effective unavailability by late afternoon. Requires daily manual restarts as a workaround, causing downtime and operational burden.","recommended_fix":"Diff the real-time notification subsystem between v2.2 and v2.3, focusing on server-side request handling paths. Look for: (1) event listeners or callbacks registered per-request that are never removed, (2) in-memory collections (notification queues, event histories, subscriber lists) that grow without bounds or eviction, (3) closures capturing request-scoped objects that prevent garbage collection. Take a heap dump from a running instance after several hours of use to identify the dominant object types consuming memory. As a quick diagnostic, check whether disabling real-time notifications halts the memory growth.","proposed_test_case":"Run a load test simulating 200 concurrent users with persistent WebSocket connections issuing ~25 API requests/user/hour for 8 hours. Assert that server memory usage remains within a bounded range (e.g., does not exceed 2x the baseline after startup stabilization). This test should be run against both v2.2 (as a control) and v2.3 to confirm the regression.","information_gaps":["Exact v2.3 changelog details beyond 'improved real-time notifications'","Server-side heap profile or dominant object types in the leaked memory","Whether the leak occurs with real-time notifications disabled","Server runtime environment (Node.js version, container memory limits, etc.)"]}

---
