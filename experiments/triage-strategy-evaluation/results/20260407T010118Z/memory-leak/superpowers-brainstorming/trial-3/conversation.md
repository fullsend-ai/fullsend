# Conversation: memory-leak x superpowers-brainstorming

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana data — that consistent memory climb is a strong signal of a leak.

To help narrow down where the leak might be, do you have a sense of what kinds of operations your 200 users are primarily doing throughout the day?

- **A)** Mostly viewing/reading tasks and dashboards
- **B)** Heavy creation and updating of tasks (lots of writes)
- **C)** Heavy use of a specific feature (e.g., real-time notifications, file attachments, search, reports)
- **D)** A mix of everything, no single dominant pattern

Also — does the memory growth rate seem to correlate with active user count, or does it climb steadily even during quieter periods?

---

### reporter

It's mostly D — a mix of everything. Our team uses TaskFlow pretty heavily across the board: viewing dashboards, creating and updating tasks, using the real-time notifications, etc. I wouldn't say any single feature dominates, though I will note that the v2.3 release we upgraded to last week mentioned 'improved real-time notifications' as a new feature, and that's right around when this problem started.

As for the growth rate — it definitely correlates with active users. During business hours (9am-5pm) when all ~200 users are active, the memory climbs steeply. On weekends when only a handful of people are on, it still grows but much more slowly. The pattern is very consistent: roughly linear growth that tracks with how many people are using the system.

---

### triage-agent

Thanks — that timing with the v2.3 upgrade is a really strong lead. Here's my working theory:

**The "improved real-time notifications" feature introduced in v2.3 is likely leaking resources** — probably WebSocket connections, event listeners, or in-memory subscription objects that aren't being cleaned up when users close their browser or navigate away. That would explain why memory growth tracks active user count (each user session adds leaked objects) and why it started right when you upgraded.

One question to help confirm this: **when a user logs out or closes their browser, does the server-side memory stay elevated, or does it drop slightly?** In other words, is the memory permanently consumed once a user has connected, or does it partially recover when users disconnect?

- **A)** Memory never goes down — it only ever increases until restart
- **B)** It dips slightly when users leave but still trends upward overall
- **C)** Not sure / haven't observed this closely enough to say

---

### reporter

A — memory never goes down. It only ever increases until we restart the server. Even overnight when almost nobody is using the system, the memory stays right where it was at end of business. It doesn't recover at all when users disconnect or log out. The only thing that brings it back down is a full server restart.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notifications: server-side resources never freed on disconnect","problem":"After upgrading to TaskFlow v2.3, the server leaks memory at a rate proportional to active user count, climbing from ~500MB to 4GB+ over a business day. Memory is never reclaimed — not when users disconnect, log out, or go idle. Only a full server restart recovers it. This started immediately after the v2.3 upgrade, which introduced 'improved real-time notifications.'","root_cause_hypothesis":"The v2.3 real-time notification system is allocating server-side resources per user session (likely WebSocket connection state, event listeners, or in-memory subscription/channel objects) that are never cleaned up when the session ends. The fact that memory doesn't drop at all on disconnect indicates missing or broken teardown logic in the connection lifecycle — the server is likely adding to a collection or registering listeners on connect but never removing them on disconnect.","reproduction_steps":["Deploy TaskFlow v2.3 on a server and monitor memory usage","Have multiple users log in and use the application normally (real-time notifications must be active)","Observe server memory climbing proportionally with number of user sessions","Have all users log out or close their browsers","Observe that server memory remains at its peak and does not decrease"],"environment":"Self-hosted TaskFlow v2.3, ~200 active users, memory monitored via Grafana","severity":"high","impact":"All 200 users experience progressively degrading performance throughout the day, with page loads exceeding 10 seconds and API timeouts by late afternoon. Requires daily manual server restarts to remain usable.","recommended_fix":"Examine the v2.3 diff for the real-time notifications feature. Look for: (1) WebSocket or SSE connection handlers that register listeners or add to collections without corresponding cleanup on disconnect/close events, (2) in-memory subscription maps or notification queues that grow per-session but are never pruned, (3) event emitter listeners added per connection without removeListener on teardown. Add proper cleanup in the connection close/disconnect handler and verify with a test that simulates connect-disconnect cycles.","proposed_test_case":"Write an integration test that opens N WebSocket/notification connections, verifies they are tracked server-side, then closes all connections and asserts that the server-side resource count (subscriptions, listeners, connection objects) returns to zero. Additionally, monitor process memory over 100+ connect-disconnect cycles and assert it stays within a bounded range.","information_gaps":["Exact server-side technology stack (Node.js, Python, etc.) — but the leak pattern is framework-agnostic and the developer will know this","Whether rolling back to pre-v2.3 resolves the issue — strongly implied by the timing but not confirmed"]}

---
