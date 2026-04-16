# Conversation: memory-leak x superpowers-brainstorming

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent memory climb pattern is really helpful.

To help narrow down the root cause: is this a recent change in behavior, or has the server always needed daily restarts?

- **Recent regression** — it started after a specific update or configuration change (if so, do you recall roughly when or what changed?)
- **Gradual onset** — it's been getting worse over time as usage grew
- **Always been this way** — daily restarts have been necessary since initial deployment
- **Something else**

---

### reporter

It's definitely a recent regression. It started about a week ago right after we upgraded from TaskFlow v2.2 to v2.3. Before the upgrade, the server would run for weeks without needing a restart and memory stayed stable. As soon as we moved to v2.3, we started seeing the daily memory climb. I noticed the v2.3 changelog mentioned 'improved real-time notifications' as one of the new features, but I'm not sure if that's related.

---

### triage-agent

That's a really strong clue — a clean regression on the v2.3 upgrade with the memory climbing linearly through the day strongly suggests a per-connection or per-event leak in the new real-time notifications system.

My working hypothesis is that the new notifications feature is opening WebSocket connections (or registering event listeners) that aren't being cleaned up when users navigate away or go idle, causing memory to accumulate proportionally to user activity throughout the day.

Could you check one thing to help confirm this? If you look at your server's active WebSocket connections (or TCP connections on the notifications port), are they growing over the day and significantly exceeding your 200 active users?

- **Yes, connections are piling up** — way more open connections than active users
- **No, connection count looks normal** — roughly tracks active users
- **Not sure how to check** — I can suggest a quick command if helpful
- **Something else**

---

### reporter

I checked and the WebSocket connection count actually looks pretty normal — it roughly tracks our active users, around 200 give or take. So it doesn't seem like connections themselves are piling up. But I did notice something interesting: when I look at the server process in more detail, the memory growth seems to correlate with API request volume rather than just the number of connected users. During lunch when fewer people are active, the memory growth slows down, and during busy periods in the morning it climbs faster. So it feels like it's tied to individual requests rather than persistent connections staying open.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3: per-request memory accumulation causes server degradation requiring daily restart","problem":"After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage climbs from ~500MB at startup to 4GB+ over the course of a day, causing page loads to exceed 10 seconds and API timeouts. The server requires a daily restart to recover. The memory growth rate correlates with API request volume, not active connection count.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature likely introduced per-request middleware or event processing that leaks memory on each API request — e.g., accumulating notification payloads, registering event listeners per request without cleanup, or caching request-scoped objects in a module-level data structure. The leak is per-request (not per-connection), since memory growth tracks request volume rather than connected users.","reproduction_steps":["Deploy TaskFlow v2.3 (upgrading from v2.2)","Allow ~200 users to interact with the system normally over a full workday","Monitor server memory usage via Grafana or process metrics","Observe memory climbing linearly from ~500MB, correlated with API request volume","By end of day, memory reaches 4GB+ and response times degrade to 10+ seconds"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2), approximately 200 active users, server monitored via Grafana","severity":"high","impact":"All 200 users experience progressively degrading performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires daily manual restart as a workaround.","recommended_fix":"1. Diff the v2.2→v2.3 changes in the real-time notifications system, focusing on any new per-request middleware, event handlers, or caching layers. 2. Take a heap snapshot under load and compare object retention between early-day and late-day to identify the accumulating objects. 3. Look for patterns like: event listeners registered per request but never removed, growing in-memory caches without eviction, notification history accumulating in a module-level array/map, or request-scoped objects captured in closures that prevent GC. 4. Verify the fix by confirming memory remains stable over an extended period under normal load.","proposed_test_case":"Write a load test that simulates a full day of API traffic (thousands of requests from 200 simulated users) against a v2.3 instance with the notifications feature active. Assert that server RSS memory stays within a bounded range (e.g., does not grow beyond 2x baseline) after N thousand requests. This test should also pass against v2.2 as a control.","information_gaps":["Exact server runtime/language (Node.js, Python, etc.) — though the investigation approach is similar regardless","Whether the real-time notifications feature can be disabled via config flag to confirm it as the source","Specific heap profile or leak trace identifying the accumulating objects"]}

---
