# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily pattern is really helpful.

I'd love to understand more about what's happening on the server throughout the day. Can you walk me through what a typical day of usage looks like for your team? For instance, are there particular features your 200 users lean on heavily — things like real-time collaboration, file attachments, recurring background jobs, or integrations with other tools? And does the slowdown seem to accelerate at any particular time, or is it a steady, gradual climb from morning to evening?

---

### reporter

Our team uses TaskFlow pretty heavily throughout the day — it's our main project management tool. The usage is fairly steady from about 9am to 5pm, nothing too spiky. People are creating and updating tasks, leaving comments, using the board views, that sort of thing. I'd estimate each user is probably hitting the app 20-30 times an hour on average.

The memory climb is very steady and linear from what I can see on Grafana — it doesn't accelerate at any particular time, it just grows consistently as long as people are using it. On weekends when fewer people are on, it still climbs but much more slowly, which tracks.

One thing I should mention — this started exactly when we upgraded from v2.2 to v2.3 about a week ago. We never had this problem on v2.2. I noticed the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, and we do have the real-time push notifications enabled. I don't know if that's related, but the timing lines up perfectly.

---

### triage-agent

That timing with the v2.3 upgrade is a really strong clue — and the fact that the growth rate scales with the number of active users makes it even more suggestive.

Here's what I'm curious about: would you be able to try disabling the real-time push notifications on a test day (or even for a few hours during a quieter period) and see whether the memory growth stops or slows dramatically? That would tell us a lot about whether the new notification system is the culprit.

Also, when the memory climbs to 4GB+ and things get slow — do the real-time notifications themselves still work at that point, or do those break down too?

---

### reporter

That's a good idea — I can definitely try disabling the real-time notifications for a few hours. I'll plan to do that tomorrow morning and keep an eye on the Grafana dashboard to see if the memory stays flat. I'll report back with what I find.

As for your second question — when things get really slow at 4GB+, the notifications technically still come through, but everything is sluggish. It's not that notifications specifically break, it's more like the entire server bogs down — API responses, page loads, notifications, all of it. It feels like the server is just overwhelmed and struggling with everything equally at that point.

---

### triage-agent

Great, looking forward to hearing the results of that test — it should be very telling.

While we wait for that, I'm curious about something on the infrastructure side: when you see the memory climbing to 4GB+, is that a single server process consuming all of it, or do you have multiple worker processes? And have you had a chance to look at anything beyond memory — for example, do the number of open connections (database connections, WebSocket connections, etc.) also climb throughout the day, or is it purely a memory phenomenon? I'm trying to get a sense of whether something is accumulating (like connections or event listeners not being cleaned up) versus the process simply caching more and more data.

---

### reporter

It's a single server process — we're running the standard self-hosted setup on an 8GB RAM Ubuntu 22.04 VM, nothing fancy with multiple workers.

That's a really good question about connections. I just checked and you're onto something — the number of WebSocket connections does seem to grow throughout the day in a way that doesn't look right. We have about 200 users, so I'd expect maybe 200 WebSocket connections at most, but the count seems much higher than that by the end of the day. I hadn't thought to look at that before, I was focused on the memory. The database connection count looks normal and stable by comparison.

So it does seem like something is accumulating, not just caching. The WebSocket connection or listener count climbing alongside the memory is suspicious, especially given the real-time notifications angle.

---

### triage-agent

[RESOLVED] {"title":"Memory leak via WebSocket connection accumulation in v2.3 real-time notification system","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server process exhibits a steady memory leak from ~500MB at startup to 4GB+ over a business day, causing severe performance degradation (10+ second page loads, API timeouts) and requiring daily restarts. The leak rate scales linearly with the number of active users.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature is not properly closing or cleaning up WebSocket connections (and/or their associated event listeners) when users navigate away, close tabs, or reconnect. Each user interaction that should reuse or replace an existing connection instead opens additional ones, causing connection count and memory to grow unboundedly throughout the day.","reproduction_steps":["Deploy TaskFlow v2.3 self-hosted with real-time push notifications enabled","Allow normal usage by ~200 users over a full business day (9am-5pm)","Monitor server process memory and WebSocket connection count via Grafana or equivalent","Observe: memory climbs linearly from ~500MB to 4GB+, WebSocket connections accumulate far beyond the active user count","Compare: same workload on v2.2 shows no memory growth and stable connection count"],"environment":"Self-hosted TaskFlow v2.3, single server process on Ubuntu 22.04 VM with 8GB RAM, ~200 active users, real-time push notifications enabled","severity":"high","impact":"All 200 users experience progressively degrading performance throughout every workday, culminating in near-unusable response times by late afternoon. The team is forced to restart the server daily as a workaround, causing downtime.","recommended_fix":"Investigate the WebSocket lifecycle management in the v2.3 real-time notification subsystem. Specifically look for: (1) connections not being closed on client disconnect/tab close/navigation, (2) reconnection logic that opens new connections without closing stale ones, (3) event listeners being attached per-connection without cleanup. Compare the connection handling code against v2.2 to identify what the 'improved real-time notifications' change introduced. Ensure connections are properly torn down and their resources freed when clients disconnect or reconnect.","proposed_test_case":"Simulate a user session that repeatedly connects, disconnects, and reconnects WebSocket connections (mimicking page navigations, tab closes, and reconnects) over several hours. Assert that the total server-side WebSocket connection count never exceeds the number of concurrently active clients, and that server memory remains stable within a bounded range.","information_gaps":["The reporter's planned test of disabling real-time notifications to confirm the memory stays flat (would confirm but not change investigation direction)","Exact WebSocket connection count at end of day vs. expected ~200 (reporter confirmed it's 'much higher' but didn't provide a specific number)","Whether the v2.3 notification system uses a different WebSocket library or connection strategy than v2.2"]}

---
