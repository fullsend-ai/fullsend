# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent daily climb is really telling.

To help us trace where the memory is going, could you tell me more about what a typical day looks like on this instance? For example: are those 200 users active throughout the day or do they arrive in waves? Are there any background processes running — scheduled reports, integrations, webhooks, or automated imports? And which version of TaskFlow are you running, and roughly how long has this been happening (did it start after an upgrade, a configuration change, or a growth in users)?

---

### reporter

We're running TaskFlow v2.3 — we upgraded from v2.2 about a week ago, and that's exactly when this started. The timing lines up perfectly with the upgrade. Our 200 users are pretty evenly spread throughout the business day, no big waves. Each person is probably making around 25 requests per hour on average, so steady usage. We don't have any special background processes or integrations running — just the standard TaskFlow setup. I did notice that the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, and I'm wondering if that might be related since that's the main thing that changed.

---

### triage-agent

That's a really useful connection you've drawn between the v2.3 upgrade and when this started — the timing is hard to ignore.

I'd like to follow the thread on those real-time notifications. Can you tell me what you see on the server side when the memory is high? For instance, if you check active connections (like WebSocket connections or long-polling requests) late in the day versus early morning, are they accumulating? And have your users actually been using the real-time notification feature, or is it just enabled in the background?

Also — have you had a chance to check whether rolling back to v2.2 makes the problem go away, or is that not practical for your team right now?

---

### reporter

Good questions. I actually did check the WebSocket connections — late in the day there are a *lot* more active WebSocket connections than there are actual users online. Early morning after a restart, the count matches roughly what I'd expect for the users logged in, but by afternoon it's way higher than the number of concurrent users. It's like connections are being opened but never closed, or something is piling up on the WebSocket side. I'm not sure exactly what's accumulating, but the numbers definitely don't add up.

As for the real-time notifications — our users aren't specifically opting into anything. It seems like it's just enabled by default in v2.3. They get little push notifications when tasks are assigned or updated, things like that. So it's running in the background on every request as far as I can tell.

We haven't rolled back to v2.2 yet. It's doable but there were a couple of database migration steps in the upgrade, so rolling back isn't trivial. I'd rather find the actual fix if possible. But if it comes to it, I could try it on a staging copy to confirm the theory.

---

### triage-agent

[RESOLVED] {"title":"WebSocket connection leak in v2.3 real-time notifications causes linear memory growth and daily restarts","problem":"After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage climbs steadily from ~500MB at startup to 4GB+ over the course of a business day, causing progressively degraded performance (10+ second page loads, API timeouts) and requiring a daily restart. The problem began exactly when v2.3 was deployed one week ago.","root_cause_hypothesis":"The real-time notification feature introduced in v2.3 opens WebSocket connections for push notifications but does not properly close or garbage-collect them when users navigate away, close tabs, or their sessions end. This causes WebSocket connections (and their associated server-side state/buffers) to accumulate throughout the day, driving linear memory growth proportional to total session events rather than concurrent users.","reproduction_steps":["Deploy TaskFlow v2.3 with default configuration (real-time notifications enabled)","Allow normal user activity over several hours (~200 users, ~25 requests/hr each)","Monitor server memory usage and active WebSocket connection count over time","Observe that WebSocket connections grow beyond the number of concurrent users and memory climbs steadily","Compare WebSocket connection count to actual logged-in users — the gap widens throughout the day"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2 one week ago), ~200 active users with steady usage throughout the business day, standard configuration with no custom integrations or background processes, monitored via Grafana","severity":"high","impact":"All 200 users experience progressively degraded performance throughout each business day. By late afternoon the application is effectively unusable (10+ second loads, API timeouts). Requires manual daily restart, creating a maintenance burden and an end-of-day availability gap.","recommended_fix":"Investigate the WebSocket lifecycle management in the v2.3 real-time notification subsystem. Specifically: (1) Check whether the server properly handles client disconnects, tab closes, and session expirations by closing the corresponding WebSocket and freeing associated state. (2) Look for missing cleanup in disconnect/close/error event handlers. (3) Check whether a heartbeat/ping-pong mechanism exists to detect stale connections and reap them. (4) Consider adding a server-side connection TTL or idle timeout as a safety net. (5) Diff the WebSocket handling code against v2.2 if any existed, or review the new notification module introduced in v2.3.","proposed_test_case":"Write an integration test that: (1) opens N WebSocket connections simulating user sessions, (2) disconnects them via various methods (clean close, abrupt TCP drop, idle timeout), (3) asserts that the server-side connection count returns to zero and memory does not grow. Additionally, a soak test running simulated traffic for several hours should show stable memory and connection counts.","information_gaps":["Exact server runtime environment (Node.js version, OS, container vs. bare metal) — may affect WebSocket library behavior but unlikely to change the fix direction","Whether the leak is per-page-navigation (new connection opened on each page load without closing the previous) or per-session (connections surviving beyond session end) — developer will determine from code inspection","Server-side error logs around WebSocket handling that might reveal failed cleanup attempts"]}

---
