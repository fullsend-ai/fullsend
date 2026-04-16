# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent memory climb is really helpful to see.

I'd like to understand the timeline around this. You mentioned you've been restarting daily for the past week — does that mean the slowdown started about a week ago, or had it been building up before that and the restarts are just a recent workaround? And if something changed around when this started (a TaskFlow upgrade, a jump in users, a change in how your team uses TaskFlow, infrastructure changes), I'd love to hear about that too, even if it doesn't seem obviously related.

---

### reporter

Good question — this started exactly when we upgraded from TaskFlow v2.2 to v2.3 about a week ago. Before the upgrade, we had no memory issues at all; the server would run for weeks without needing a restart. Nothing else changed — same number of users, same VM, same infrastructure. The only thing that changed was the TaskFlow version. I noticed the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, and I've been wondering if that might be related, but I don't have any hard evidence for that — just the timing.

---

### triage-agent

That's a really strong lead — the timing lining up perfectly with v2.3 makes the 'improved real-time notifications' feature a prime suspect. I'd like to explore that thread a bit more.

Can you tell me about how your users typically interact with TaskFlow during the day? For example, do they tend to keep TaskFlow open in a browser tab all day, or do they open it, do something, and close it? And have you noticed whether the memory growth correlates with the number of users currently connected — say, does it plateau on weekends or evenings when fewer people are logged in, or does it just keep climbing regardless?

---

### reporter

Most of our users open TaskFlow in the morning and keep it open in a browser tab throughout the day — they're in and out of it pretty frequently, creating tasks, updating statuses, checking dashboards, that sort of thing. It's a busy team so there's a lot of activity during business hours.

As for the correlation — yes, actually, the memory growth does seem to track with activity levels. On the weekend we had the server running and the memory barely climbed at all, maybe a few hundred MB over two days. But on a weekday with everyone active, it'll go from 500MB to 3-4GB over about 8 hours. So it definitely seems tied to how much the server is being used, not just time passing. The more requests hitting the server, the faster the memory grows. I haven't done exact math on it, but the Grafana graphs make it pretty obvious — the slope is much steeper during peak hours than during lunch or after hours.

---

### triage-agent

Thanks — that weekend vs. weekday comparison is really telling. Let me share where my thinking is and see if it lines up with what you're seeing.

It sounds like the v2.3 "improved real-time notifications" feature is likely allocating something on each request (or each notification event) that never gets cleaned up. The fact that memory growth tracks with *activity volume* rather than just the number of people connected suggests it's not simply "200 open WebSocket connections slowly leaking" — it's more like every action that *triggers* a notification leaves something behind in memory.

Does that match your intuition? And one more thing that would help narrow this down for a developer: on your Grafana dashboard or server metrics, do you happen to track the number of active connections (e.g., WebSocket connections) or thread/process counts? I'm curious whether those also grow unbounded through the day, or whether it's *only* memory that climbs while connection counts stay relatively flat.

---

### reporter

Yeah, that matches my intuition exactly — it does feel like every action is leaving something behind rather than just the connections themselves being the problem. Our users are making a lot of requests throughout the day, so if each one is leaking something small, it would add up fast.

As for connection metrics — I do have some Grafana panels for that. The WebSocket connection count stays pretty stable during the day, roughly tracking the number of active users (around 150-200 during business hours, dropping off in the evening). It doesn't grow unbounded like the memory does. So it's definitely not the connections themselves piling up. It's only the memory that keeps climbing. That's actually something I noticed early on and it confused me — I expected if it were a connection leak, I'd see connection counts rising too, but they look normal.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notifications causes server degradation requiring daily restart","problem":"After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage grows unbounded from ~500MB at startup to 4GB+ over an 8-hour business day, causing page loads to exceed 10 seconds and API timeouts. The server must be restarted daily as a workaround. The leak rate correlates with request/activity volume, not with elapsed time or connection count.","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 is allocating resources (e.g., event handlers, notification payloads, subscriber lists, or message buffers) on each notification-triggering action that are never freed. Since WebSocket connection counts remain stable while memory climbs, the leak is per-event rather than per-connection — every user action that generates a notification leaves behind a small allocation that accumulates over the day.","reproduction_steps":["Deploy TaskFlow v2.3 on a server and note baseline memory (~500MB)","Simulate or allow normal business-day activity from ~150-200 concurrent users performing typical actions (creating tasks, updating statuses, checking dashboards)","Monitor server memory via Grafana or similar over several hours","Observe memory climbing steadily in proportion to request volume, reaching 3-4GB after ~8 hours of typical activity","Confirm WebSocket connection counts remain stable and do not grow unbounded","Optionally compare against v2.2 under identical load to confirm the leak is absent"],"environment":"Self-hosted TaskFlow v2.3, ~200 active users, single VM (specs not specified), users keep browser tabs open throughout the day. Previously stable on v2.2 for weeks without restart.","severity":"high","impact":"All 200 users experience progressively degrading performance throughout the workday, with the application becoming effectively unusable by late afternoon. Requires manual daily server restarts, causing downtime and operational burden.","recommended_fix":"Investigate the real-time notifications code path changed in v2.3. Take a heap dump or memory profile during active use and compare to v2.2. Look for per-event allocations that accumulate: event listener registrations that aren't removed, notification objects stored in an unbounded in-memory collection, subscriber tracking structures that grow per-event rather than per-connection, or message history/buffers that are never pruned. The fix likely involves ensuring per-notification allocations are released after delivery or bounded by a cap/TTL.","proposed_test_case":"Write a load test that simulates 200 concurrent users performing notification-triggering actions over a sustained period (e.g., 1000 actions per user). Assert that server memory usage remains within a bounded range (e.g., does not exceed 2x baseline) after all actions complete and a GC cycle is triggered. Additionally, add a unit test for the notification subsystem confirming that per-event allocations are released after notification delivery.","information_gaps":["Server runtime/language (Node.js, Java, Python, etc.) — determines which profiling tools to use","Whether disabling the real-time notifications feature (if possible via config) stops the leak — would confirm the hypothesis definitively","Exact v2.3 changelog diff for the notifications feature"]}

---
