# Conversation: memory-leak x omc-deep-interview

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily memory climb is a strong signal.

To help narrow down the source of the leak: what does your TaskFlow deployment look like? Specifically:
- Which version of TaskFlow are you running, and did this start after a particular upgrade?
- Are you running background jobs or workers (e.g., notifications, scheduled task reminders, integrations) alongside the main server process, or is it all in one process?
- What database are you using, and does its connection count or memory also grow over the day, or is the growth isolated to the TaskFlow application process?

---

### reporter

We're running TaskFlow v2.3 — we upgraded from v2.2 about a week ago, and now that you mention it, the problems started right after that upgrade. Before v2.3 we didn't have this issue at all.

It's all running as a single process on an Ubuntu 22.04 VM with 8GB RAM. We do use the real-time notifications feature — that was actually one of the things highlighted in the v2.3 changelog as 'improved real-time notifications,' which is part of why we upgraded.

We're using the default PostgreSQL setup. I haven't noticed the database memory growing unusually — the growth seems to be entirely in the TaskFlow application process itself. The DB connection count looks normal in Grafana.

---

### triage-agent

That timing with the v2.3 upgrade is very telling — especially the real-time notifications connection.

Could you check one thing for me? In your Grafana dashboard (or via `ss` / `netstat` on the server), do the number of open WebSocket or long-lived HTTP connections grow throughout the day, or do they stay roughly proportional to your active user count? Specifically, I'm wondering whether connections from the real-time notifications system are accumulating rather than being cleaned up when users close their browsers or go idle.

Also — if it's easy to test — have any of your users disabled real-time notifications in their settings? If so, does the leak still happen on a day when notification-heavy activity is low (e.g., a weekend)?

---

### reporter

Good question — I just checked and this is interesting. The WebSocket connection count actually looks pretty stable and proportional to our active users. It hovers around 180-200 during the workday and drops off in the evening as people log off. So it's not like connections are piling up unclosed.

However, I did notice something else while poking around: the memory growth rate seems to correlate more with overall API request volume than with the number of connected users. On weekends when we have maybe 30-40 active users but they're still making requests, the memory still climbs — just more slowly. So it's not specifically tied to how many people are connected via WebSocket, it seems more like every API request is leaving something behind.

As for disabling notifications — I don't think any of our users have turned them off. Everyone likes the real-time updates. I could ask a few people to try disabling them as a test if that would help narrow it down.

---

### triage-agent

That's a really useful finding — ruling out WebSocket accumulation and pinpointing API request volume as the driver narrows things down a lot.

To help a developer reproduce this quickly rather than waiting a full day: could you check whether the memory growth is tied to specific types of API requests? For example, if you look at your access logs or Grafana, do certain endpoints stand out in volume — things like task creation, search/filtering, file attachments, or notification-related API calls?

Alternatively, if it's easy to do, could you try running a simple load test against just one or two common endpoints (e.g., listing tasks, creating a task) on a fresh restart and see if memory climbs? Even 10-15 minutes of sustained requests to a single endpoint would tell us whether the leak is across the board or isolated to specific operations. That would let a developer zero in on the right code path immediately.

---

### reporter

I took a look at our access logs and Grafana — honestly, I don't see any single endpoint standing out as the culprit. The request volume is spread pretty evenly across the usual stuff: listing tasks, updating task status, loading project boards, etc. The memory growth seems to track with total request volume regardless of which endpoints are being hit. We do about 25 requests per user per hour on average, so with 200 users that's roughly 5,000 requests/hour during the workday.

I haven't done a formal load test, but I can try that tomorrow morning on a fresh restart if you think it would help. Based on what I'm seeing though, my gut feeling is it's something that happens on every request rather than being tied to a specific endpoint — like some per-request resource that's not getting cleaned up. The fact that it started exactly with v2.3 and the 'improved real-time notifications' feature makes me wonder if the notification system is hooking into every API request somehow and leaving something behind.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3: per-request resource accumulation causes progressive slowdown requiring daily restart","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server process leaks memory on every API request. Memory grows from ~500MB at startup to 4GB+ over a workday (~5,000 requests/hour with 200 users), causing page loads >10s and API timeouts by late afternoon. The server requires a daily restart to recover.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature likely introduced middleware or a per-request hook that allocates resources (event listeners, notification context objects, or subscription tracking structures) on every API request without releasing them. This is supported by: (1) the leak started exactly with the v2.3 upgrade, (2) it correlates with total API request volume regardless of endpoint, (3) WebSocket connection counts are stable and proportional to active users, and (4) the database is not affected. The leak is in the application process, not in connections or the DB layer.","reproduction_steps":["Deploy TaskFlow v2.3 on a clean VM with default PostgreSQL setup","Start the server and record baseline memory (~500MB expected)","Generate sustained API traffic against any common endpoints (list tasks, update status, load boards) at ~5,000 requests/hour","Monitor process memory over 1-2 hours — expect steady linear growth","Compare by repeating the same test with TaskFlow v2.2 to confirm no leak"],"environment":"TaskFlow v2.3 (upgraded from v2.2), single-process deployment on Ubuntu 22.04 VM with 8GB RAM, default PostgreSQL, real-time notifications enabled, ~200 active users","severity":"high","impact":"All 200 users experience progressive degradation daily. By afternoon, the application is effectively unusable (10s+ loads, API timeouts). Requires manual daily restart as a workaround, which disrupts evening/off-hours users.","recommended_fix":"Diff the v2.3 changes against v2.2, focusing on the real-time notifications overhaul and any new middleware or request hooks. Look for per-request allocations that are never freed: event listener registrations, notification context objects appended to a growing collection, subscription maps keyed by request/session that are never evicted, or closures capturing request-scoped data in a long-lived scope. A heap snapshot comparison between startup and after ~10,000 requests should pinpoint the leaking object type. As an immediate mitigation, the reporter can downgrade to v2.2.","proposed_test_case":"Run a load test sending 50,000 API requests to a fresh v2.3 instance and assert that process RSS memory stays below a defined ceiling (e.g., startup + 200MB). The same test against v2.2 should pass, confirming the regression. After the fix, the v2.3 test should also pass.","information_gaps":["Exact object type accumulating in memory (requires heap profiling by a developer)","Whether the leak is in notification middleware specifically or a broader request-lifecycle change in v2.3","Whether disabling real-time notifications for all users would stop the leak (viable workaround if so)"]}

---
