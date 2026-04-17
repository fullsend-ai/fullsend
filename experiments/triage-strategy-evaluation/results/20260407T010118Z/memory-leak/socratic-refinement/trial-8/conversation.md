# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent memory climb pattern is really helpful.

To start narrowing this down: can you tell me more about what your team's typical day looks like in TaskFlow? For instance, are there particular features or workflows your 200 users lean on heavily (e.g., bulk task imports, real-time notifications, large file attachments, reporting dashboards)? And does the slowdown seem to correlate with peak usage times, or does the memory just climb steadily regardless of how many people are active?

---

### reporter

Our users mostly create and update tasks, leave comments, and use the real-time notifications pretty heavily — people like getting instant updates when tasks they're watching get modified. We don't do much with bulk imports or large file attachments. The memory climb is very steady and linear, it doesn't really spike during peak hours. It just grows at a consistent rate from the moment the server starts, proportional to overall traffic. Even on weekends when we have fewer users, it still climbs, just more slowly. One thing I should mention — we upgraded from v2.2 to v2.3 about a week ago, and I think that's when this started. The v2.3 changelog mentioned 'improved real-time notifications' which is something our team was excited about.

---

### triage-agent

That's a really valuable detail about the v2.3 upgrade timing — it lines up well with what you're seeing.

I'd like to dig a bit more into the real-time notifications side of things. When your users are watching tasks and receiving instant updates, do those notification connections stay open all day (e.g., do people keep TaskFlow open in a browser tab throughout their workday), or do they tend to open and close it frequently? And have you noticed whether the memory on the server correlates at all with the number of users who are currently connected — for example, does the memory ever drop back down when people log off for lunch, or does it only ever go up?

---

### reporter

Most people keep TaskFlow open in a browser tab all day, yeah. But the memory never drops back down — that's the weird part. Even during lunch when half the office is away from their desks, the memory stays at whatever level it climbed to and just keeps going up. It only goes back down when we restart the server. So it doesn't seem tied to how many people are currently connected, it's more like every interaction leaves something behind that never gets cleaned up. The growth rate does slow down when fewer people are active (like evenings or weekends), but it never decreases on its own.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in real-time notification system introduced in v2.3 causes progressive server degradation","problem":"After upgrading to v2.3, the TaskFlow server's memory grows linearly from ~500MB to 4GB+ over the course of a day, causing page loads to exceed 10 seconds and API timeouts. Memory never reclaims without a full server restart, even when connected users decrease.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' change is likely not cleaning up WebSocket/SSE connections or their associated event subscriptions and listeners when clients disconnect or navigate away. Each interaction allocates resources (connection handlers, notification subscriptions, event listener callbacks) that are never freed, causing monotonic memory growth proportional to cumulative traffic rather than concurrent connections.","reproduction_steps":["Deploy TaskFlow v2.3 on a self-hosted instance","Start the server and note baseline memory (~500MB)","Have users connect and interact normally — create/update tasks, leave comments, with real-time notifications enabled","Monitor memory over several hours; observe steady linear growth","Note that memory does not decrease when users disconnect or go idle","Compare with v2.2 under the same workload to confirm the regression"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2 approximately one week ago), ~200 active users, most keeping TaskFlow open in browser tabs all day, heavy use of real-time notifications","severity":"high","impact":"All 200 users on this instance experience progressively degraded performance daily, with the application becoming effectively unusable by late afternoon. Requires manual daily restart as a workaround.","recommended_fix":"Diff the real-time notification code between v2.2 and v2.3. Look for: (1) WebSocket or SSE connection handlers that don't clean up on disconnect/close events, (2) event listener or subscription registrations without corresponding removal on connection teardown, (3) in-memory data structures (maps, arrays, caches) tracking connected clients or pending notifications that grow without bounds. Add proper cleanup in connection close/error handlers and consider a periodic sweep for stale entries.","proposed_test_case":"Write an integration test that simulates N clients connecting, subscribing to task notifications, performing interactions, then disconnecting. After all clients disconnect, assert that memory usage (or the size of internal tracking structures) returns to near-baseline levels. Run this in a loop to verify no cumulative growth over multiple connect/disconnect cycles.","information_gaps":["No heap dump or memory profile to confirm which objects are accumulating","Exact server runtime and framework (Node.js, Python, etc.) not specified","Whether the v2.3 notification change used a new library or refactored existing code is unknown"]}

---
