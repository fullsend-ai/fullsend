# Conversation: memory-leak x omo-prometheus

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana data — that consistent daily memory ramp is a strong signal.

To start narrowing this down: **is the memory growth you're seeing in the TaskFlow application process itself, or at the OS/host level?** Specifically:

1. When memory hits 4GB+, have you checked which process is consuming it? (e.g., `top`, `htop`, or container metrics if dockerized) — is it the TaskFlow server process, the database, a background worker, or something else?
2. Is this a recent regression, or has it always behaved this way? If recent, did anything change — a version upgrade, config change, or increase in user count?

---

### reporter

Good questions! Yes, the memory is definitely in the TaskFlow server process itself — I've been watching it in htop and it's the main TaskFlow process that balloons. The database stays pretty stable.

As for timing, this started exactly when we upgraded from v2.2 to v2.3 about a week ago. We never had this issue on v2.2 — the server would run for weeks without needing a restart. The upgrade was the only change we made; user count has been stable at around 200 for months.

---

### triage-agent

That's very helpful — the v2.2 → v2.3 regression is a strong lead.

Two things that would help me narrow this down significantly:

1. **Does the memory growth correlate with user activity, or does it happen even when the system is idle?** For example, if you restarted the server on a weekend or off-hours when few people are using it, does the memory still climb at the same rate?

2. **Do you have access to the v2.3 changelog or release notes?** Specifically, did v2.3 introduce any changes to caching, WebSocket handling, background jobs, or session management? Any of those are common sources of memory leaks.

If you're not sure about #1, even a rough observation like "it seems slower on busy days" vs. "it climbs the same regardless" would help.

---

### reporter

For #1 — it definitely correlates with user activity. On the weekend when only a handful of people are logged in, the memory grows much more slowly. On a normal weekday with all ~200 users active, it climbs steadily and hits that 3-4GB range by end of day. So it's clearly tied to usage volume.

For #2 — yes, I looked at the v2.3 changelog when this started happening. The main thing that caught my eye was a feature listed as 'improved real-time notifications.' I remember that because we were actually excited about it — our users had been asking for better push notifications. Beyond that there were some minor bug fixes but nothing else that seemed like it could cause this. I don't recall seeing anything about caching or session changes specifically.

---

### triage-agent

Thanks — the "improved real-time notifications" feature in v2.3 combined with the user-activity-correlated memory growth is a very strong signal. This looks like a classic connection or event-listener leak in the real-time notification subsystem.

To confirm that theory: **can you check how many open WebSocket (or long-polling) connections the TaskFlow server is holding?** A few ways to check:

- `ss -s` or `netstat -an | grep ESTABLISHED | wc -l` on the server — compare the count right after a restart vs. late in the day when memory is high
- If TaskFlow exposes any internal metrics or a `/debug` endpoint, check for active connection counts

Specifically, I'm looking for whether the connection count grows over time and never drops — even after users log out or close their browsers. That would confirm connections are being opened for notifications but never properly closed.

---

### reporter

I just checked this. Right after a fresh restart this morning, `netstat -an | grep ESTABLISHED | wc -l` showed about 220 connections, which makes sense for our ~200 users. Late in the afternoon when memory was already around 3GB, the established connection count was around 230 — so not dramatically different. The WebSocket connections themselves don't seem to be piling up.

However, I did notice something interesting while poking around. The server does expose a `/debug/vars` endpoint, and there's a counter for notification event listeners. After the morning restart it was around 200 (roughly one per user), but by the time I checked it late in the day it was over 40,000. That seems way too high. It looks like listeners are being registered but never removed — every time a user makes an API call, the listener count ticks up and it never goes back down. So it might not be the connections themselves leaking, but the event listeners attached to them.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification subsystem: event listeners registered per API call but never removed","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server process leaks memory steadily during normal usage, climbing from ~500MB at startup to 4GB+ over a workday with ~200 active users. Page loads degrade to 10+ seconds and API calls time out, requiring a daily server restart.","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 registers a notification event listener on every API call but never removes it. Over a day of normal usage (~200 users making repeated API calls), the listener count grows from ~200 to 40,000+, each retaining references that prevent garbage collection. The connections themselves are managed correctly (count stays stable at ~220-230), but the listeners attached to them accumulate without bound.","reproduction_steps":["Deploy TaskFlow v2.3 (the bug does not exist in v2.2)","Start the server and note baseline memory (~500MB) and listener count (~200 via /debug/vars)","Allow normal user activity with ~200 users over several hours","Monitor memory via htop and listener count via /debug/vars","Observe memory climbing steadily and listener count growing with every API call, never decreasing","By end of day, memory reaches 4GB+ and listener count exceeds 40,000"],"environment":"Self-hosted TaskFlow v2.3, ~200 active users, server monitored via Grafana and htop. Regression from v2.2 where the server ran stably for weeks.","severity":"high","impact":"All ~200 users experience progressively degrading performance throughout each workday (10+ second page loads, API timeouts by late afternoon). Requires manual daily restart as a workaround. No data loss, but significant productivity impact.","recommended_fix":"Diff the real-time notification subsystem between v2.2 and v2.3. Look for event listener registration in API request handlers (middleware, decorators, or notification subscription logic) that lacks a corresponding teardown/removal. Likely fix: ensure listeners are either (a) registered once per connection/session rather than per API call, or (b) properly removed after use. Check for patterns like `emitter.on()` without a corresponding `emitter.removeListener()` or `emitter.off()`, or subscription callbacks added inside request handlers without cleanup.","proposed_test_case":"Write a load test that simulates 50+ users making repeated API calls over a sustained period. Assert that the notification event listener count (from /debug/vars) stays proportional to the number of active connections/sessions and does not grow with the number of API calls. Also assert that memory usage remains within a bounded range (e.g., does not exceed 2x baseline) after 10,000 API calls.","information_gaps":["Exact code path where listeners are registered (requires reading the v2.3 diff)","Whether the leak rate varies by API endpoint or is uniform across all endpoints","Whether a config option or feature flag can disable the new notification system as a temporary mitigation beyond daily restarts"]}

---
