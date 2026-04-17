# Conversation: memory-leak x omc-deep-interview

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent daily memory climb is a strong signal.

To help narrow down the source of the leak: could you share which TaskFlow components you're running and how they're deployed? Specifically:
- Which version of TaskFlow are you on?
- Are you running the default configuration, or have you enabled any plugins, integrations, or background jobs (e.g., webhook listeners, email notifications, calendar sync)?
- What's your database setup (type, connection pooling config if any)?

This will help us figure out where the memory is accumulating.

---

### reporter

We're on TaskFlow v2.3 — we upgraded from v2.2 about a week ago, which is actually right around when this problem started. I hadn't connected the two until you asked, but the timing lines up pretty closely.

We're running a fairly standard setup. No third-party plugins. We do have the real-time notifications enabled (push notifications via WebSocket), which I believe was one of the features that got improved in v2.3. No calendar sync or email integration — just the core app with the built-in notification system.

Database is PostgreSQL with the default connection pooling config that ships with TaskFlow. The database server itself looks fine — CPU and memory on that box are stable. It's specifically the TaskFlow application server that's climbing in memory.

The server is an 8GB RAM VM running Ubuntu 22.04.

---

### triage-agent

That timing with the v2.3 upgrade is really helpful — that's likely our culprit.

Since the real-time notifications (WebSocket) feature was reworked in v2.3, I'd like to understand the memory growth pattern a bit better: does the memory climb track with user activity, or is it purely time-based? For example, on weekends or lighter-traffic days, does memory still climb at the same rate, or does it grow more slowly? And have you noticed whether the number of active WebSocket connections on the server grows over the day (some monitoring tools or `ss -s` output would show this), or does it stay roughly proportional to the number of logged-in users?

This will help us figure out whether connections are leaking (not closing when users disconnect) versus something accumulating per-request.

---

### reporter

Good question. It definitely tracks with user activity. We don't have many weekend users, and on Saturday/Sunday the memory barely moves — maybe a few hundred MB over the whole day. Monday through Friday it climbs steadily and the rate correlates pretty well with peak hours. Mid-morning and early afternoon when everyone's active is when we see the steepest climbs on the Grafana graph.

As for WebSocket connections, I actually checked that. The number of active WebSocket connections stays pretty stable and proportional to logged-in users — around 150-180 during the workday. They do close properly when people log out or go idle. So it doesn't look like connections themselves are leaking. Whatever is accumulating seems to be happening while connections are active, not because they're failing to close.

---

### triage-agent

This is really useful — the fact that memory growth tracks with user activity but WebSocket connections themselves close properly strongly suggests something is accumulating per-notification rather than per-connection. The v2.3 notification rework is looking like our prime suspect.

Would you be able to test with real-time notifications disabled for a day? In TaskFlow's config, you should be able to set `realtime_notifications: false` (users would fall back to polling). If memory stays flat with that off, we'd have strong confirmation that the leak is in the notification pathway, and that would also give you a workaround while we track down the fix.

If that's not feasible in production, even a few hours during a busy period would be informative — we'd expect to see the Grafana memory curve flatten out.

---

### reporter

That's a great idea — I can try that tomorrow morning. I'll set `realtime_notifications: false` before the workday starts and monitor the Grafana dashboard through the day. If the memory stays flat, that would definitely confirm it's the notification system. I'll report back with the results.

In the meantime, our current workaround of restarting every evening is keeping things usable, so we can afford to run a day without real-time notifications for testing purposes. Users will just have to deal with polling for a day.

---

### triage-agent

That test plan sounds perfect — looking forward to those results.

While you're at it, one more thing that would really accelerate the fix: when memory is high (say, late afternoon before your restart), could you capture a heap dump or memory profile of the TaskFlow process? Depending on your runtime, something like `kill -USR1 <pid>` for Node.js (generates a heap snapshot) or the equivalent for your stack would work. If that's not feasible, even the output of the process's GC stats or `/debug/pprof/heap` endpoint (if TaskFlow exposes one) would help.

A heap dump would tell developers exactly what type of objects are piling up — notification payloads, event listeners, serialized messages, etc. — and point straight to the leaking code path rather than requiring them to guess.

---

### reporter

Sure, I can try to grab that. I'm not 100% sure what runtime TaskFlow uses under the hood — I'm more on the sysadmin side than the development side. But I'll check if there's a `/debug/pprof/heap` endpoint available tomorrow afternoon when memory is high, and if that works I'll attach the output. If not, I'll look into the `kill -USR1` approach or whatever is appropriate for TaskFlow's stack. I should be able to do this right before I run the notification-disabled test, actually — capture a heap dump on the last "normal" day with notifications on, then switch to `realtime_notifications: false` the next day and compare. I'll report back with both the heap dump (if I can get one) and the results of the notification test.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification pathway causes progressive server degradation","problem":"The TaskFlow application server exhibits a steady memory leak, climbing from ~500MB at startup to 4GB+ over a workday, causing page loads >10s and API timeouts. The issue began coinciding with the upgrade from v2.2 to v2.3 approximately one week ago and requires a daily server restart as a workaround.","root_cause_hypothesis":"The real-time notification (WebSocket) subsystem reworked in v2.3 is accumulating objects in memory on a per-notification basis. WebSocket connections themselves open and close correctly, but something in the notification processing path — likely notification payloads, event listener registrations, serialized message buffers, or subscription tracking structures — is being retained and not garbage collected after delivery. The leak rate correlates with user activity volume, not connection count.","reproduction_steps":["Deploy TaskFlow v2.3 with default configuration and realtime_notifications enabled","Connect to a PostgreSQL database with default connection pooling","Simulate sustained activity from ~150-200 concurrent users generating notifications over several hours","Monitor application server memory — expect steady linear growth correlating with notification throughput","Compare against the same workload on v2.2 to confirm the regression"],"environment":"TaskFlow v2.3, self-hosted on Ubuntu 22.04, 8GB RAM VM, PostgreSQL with default connection pooling, ~200 active users, ~150-180 concurrent WebSocket connections during peak hours","severity":"high","impact":"All 200 active users experience progressive performance degradation daily, with the application becoming effectively unusable by late afternoon. Currently mitigated by daily evening restarts. No data loss, but significant productivity impact across the entire user base.","recommended_fix":"Diff the real-time notification subsystem between v2.2 and v2.3 — focus on notification dispatch, event listener lifecycle, message serialization/buffering, and any new in-memory caching or subscription tracking introduced in the rework. Look for objects retained after notification delivery: uncleared listener arrays, growing maps/caches without eviction, or closures capturing large scopes. A heap dump (reporter is capturing one) will pinpoint the exact object type accumulating. As an immediate workaround, users can set `realtime_notifications: false` to fall back to polling.","proposed_test_case":"Run a load test simulating 200 users with continuous notification activity over 8 simulated hours. Assert that application server RSS memory stays within a bounded range (e.g., does not exceed startup memory + 500MB) and that no single object type's instance count grows monotonically over the test duration.","information_gaps":["Heap dump has not yet been captured — would pinpoint the exact leaking object type and code path","The notification-disabled confirmation test has not yet been run — would definitively confirm the notification subsystem as the sole source","Exact TaskFlow runtime (Node.js, Go, etc.) is unknown to the reporter, which slightly affects debugging instructions"]}

---
