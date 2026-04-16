# Conversation: memory-leak x omc-deep-interview

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily memory climb is a strong signal.

To help narrow down what might be causing the leak: what version of TaskFlow are you running, and could you share a bit about your deployment setup? Specifically, I'm curious whether you're using any background features like webhooks, scheduled task reminders, or integrations with external services (email, Slack, etc.) that might be accumulating connections or data in memory over the course of the day.

---

### reporter

We're running TaskFlow v2.3 — we upgraded from v2.2 about a week ago, which is actually right around when this problem started. The server is an 8GB RAM VM running Ubuntu 22.04.

As for integrations, we do use the real-time notifications feature (push notifications in the browser), but we don't have any external integrations like Slack or email set up. No webhooks either. The real-time notifications feature was actually one of the things mentioned as 'improved' in the v2.3 changelog, now that I think about it. We didn't have this memory issue on v2.2.

---

### triage-agent

That's a really helpful detail about the timing lining up with the v2.3 upgrade — and the real-time notifications change is a strong lead.

One thing that would help narrow this down further: does the memory growth seem to correlate with how many users are actively online (i.e., have TaskFlow open in their browser)? For example, does memory still climb on weekends or off-hours when fewer people are connected, or does it plateau when nobody's using it?

---

### reporter

Yes, it definitely correlates with usage. On the weekend we had maybe 20-30 users online and the memory growth was much slower — it barely climbed over the two days. But on Monday as soon as everyone was back, it shot right back up to the same pattern. During off-hours in the evening after most people log off, it seems to plateau and stay roughly where it was. So the more users actively making requests, the faster it climbs. That's consistent with what we see on our Grafana dashboard — the slope of the memory graph is steepest during peak hours mid-morning and early afternoon.

---

### triage-agent

This is shaping up to look like a connection or session leak in the v2.3 real-time notifications system — great catch on the changelog timing.

One more thing that would really help a developer jump straight into debugging: could you check whether WebSocket or SSE connections are accumulating on the server? Something like running `ss -s` or `netstat -an | grep ESTABLISHED | wc -l` a few times during the day would tell us if the connection count keeps growing even as users come and go. If the number of established connections is way higher than the number of currently active users, that would confirm the notification system isn't cleaning up after disconnected sessions.

Alternatively, if that's not easy to check — have you noticed whether the memory *drops at all* when a large group of users logs off (like at lunchtime), or does it only ever go up and plateau?

---

### reporter

I actually did run `ss -s` a couple times yesterday out of curiosity. The number of established connections looked roughly in line with how many users were online — it wasn't growing out of control or anything. So I don't think it's connections themselves piling up.

As for your second question — no, the memory never drops. It only ever goes up or plateaus. At lunchtime when a bunch of people step away, the climb slows down but it doesn't reclaim any memory. Same in the evening — once people log off it just flatlines at whatever level it reached. The only way to get the memory back is a full server restart. That's what made me think it's a leak rather than just high usage.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification system causes progressive slowdown requiring daily restart","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory proportional to user activity. Memory climbs from ~500MB at startup to 4GB+ over a workday with 200 active users, causing 10+ second page loads and API timeouts. Memory never reclaims — it only grows during activity and plateaus during idle periods. A full server restart is the only way to recover.","root_cause_hypothesis":"The v2.3 changes to the real-time notifications system introduced a memory leak where per-user or per-event data structures are allocated on each interaction but never freed when users disconnect or events are delivered. The fact that established connection counts remain normal (ruling out socket/connection leaks) while memory only grows and never reclaims points to an in-process data structure (e.g., an event history buffer, subscriber registry, or notification queue) that accumulates entries without eviction or cleanup.","reproduction_steps":["Deploy TaskFlow v2.3 on a test VM with real-time notifications enabled","Simulate 50-200 concurrent users performing normal task operations (creating, updating, completing tasks) over several hours","Monitor process memory (RSS) — it should climb steadily and never decrease even as simulated users disconnect","Compare with the same test on v2.2 to confirm the regression"],"environment":"TaskFlow v2.3, self-hosted on 8GB RAM VM running Ubuntu 22.04, ~200 active users, real-time browser push notifications enabled, no external integrations (no Slack, email, or webhooks)","severity":"high","impact":"All 200 active users experience progressive degradation daily. By late afternoon the application is effectively unusable (10+ second loads, API timeouts). Operations team must restart the server every evening. Productivity loss is significant and ongoing.","recommended_fix":"Diff the real-time notification subsystem between v2.2 and v2.3. Look for in-memory collections (subscriber maps, event buffers, notification queues) that grow per-user or per-event without cleanup. Likely candidates: (1) a notification history or event log that appends but never trims, (2) a subscriber/session registry that adds entries on connect but doesn't remove them on disconnect, or (3) per-user message queues that are allocated but never garbage-collected. Add proper cleanup on WebSocket/SSE disconnect and consider bounded data structures (e.g., ring buffers or TTL-based eviction) for any event history.","proposed_test_case":"Write a load test that connects N simulated users, has them perform actions generating notifications, then disconnects them. Assert that after all users disconnect and a GC cycle is forced, server memory returns to within a small margin of baseline. Run this for multiple connect/disconnect cycles to verify no accumulation.","information_gaps":["No heap dump or memory profiler output to pinpoint the exact leaking object type","Haven't confirmed whether rolling back to v2.2 resolves the issue (though timing correlation is strong)","Unknown whether the leak rate scales linearly with user count or with notification volume specifically"]}

---
