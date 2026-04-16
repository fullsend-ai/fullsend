# Conversation: memory-leak x omc-deep-interview

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily memory climb is a strong signal.

To help narrow down where the memory is going: can you tell us which TaskFlow server components you're running and how they're deployed? Specifically:

- Are you running the all-in-one deployment, or are the API server, background workers, and WebSocket server split out?
- What database are you using (Postgres, MySQL), and are you using the built-in connection pooling or an external pooler?
- Have you noticed whether the memory growth is in the main server process, or could it be in a background worker or queue?

Also — did this problem start recently, or has it been happening since you first deployed? If it started recently, did anything change around that time (upgrade, config change, increase in users)?

---

### reporter

We're running the all-in-one deployment — everything on a single Ubuntu 22.04 VM with 8GB RAM. We use Postgres for the database with the built-in connection pooling (default settings). I haven't split out any components.

The memory growth is definitely in the main server process. I've been watching it in top/htop and it's the TaskFlow process itself that climbs steadily.

This started exactly one week ago when we upgraded from v2.2 to v2.3. Before the upgrade we had no issues at all — the server would run for weeks without needing a restart. I noticed the v2.3 changelog mentioned 'improved real-time notifications' as one of the new features, not sure if that's related. No other changes were made — same config, same user base, same VM.

---

### triage-agent

That's really helpful — the fact that this started exactly with the v2.3 upgrade is a strong clue, and your instinct about the real-time notifications feature is worth investigating.

Could you check how many WebSocket connections are active on the server at different points in the day? For example, compare the count in the morning vs. late afternoon when things slow down. You can check with:

```bash
ss -s | grep estab
# or more specifically for the TaskFlow port:
ss -tn state established '( dport = :YOUR_TASKFLOW_PORT )' | wc -l
```

Also — when users close their browser tabs or log out, do you know if the connections are being cleaned up, or do they seem to accumulate? If you have access to the TaskFlow admin panel or logs, any connection/session counts there would be useful too.

---

### reporter

I just ran those commands. The established connection count doesn't seem to grow dramatically — it stays roughly proportional to the number of users online, maybe 150-220 connections throughout the day. When users close their tabs, the WebSocket connections do seem to drop off normally.

What's interesting though is that even after connections close, the memory doesn't go back down. It just keeps climbing. So it doesn't seem like the connections themselves are accumulating — it's more like something is being left behind after each request or connection.

I also checked the TaskFlow logs and I don't see any errors, but I did notice a lot of log lines about registering notification listeners for API requests. The volume of those log entries seems really high — way more than I'd expect. I don't remember seeing those in v2.2. Could that be related to the 'improved real-time notifications' feature they added?

---

### triage-agent

Great find on the notification listener log lines — that's a really promising lead.

Could you dig into those a bit more? Specifically:

1. Are the "registering notification listener" messages appearing for *every* API request (including simple GETs like loading a task list), or only for specific actions like creating/updating tasks?
2. Is there a corresponding "deregistering" or "removing" listener message when the request completes, or do you only ever see the registration side?
3. If you're able to, could you check how many notification listeners are registered at a given moment? Something like the TaskFlow admin panel or debug endpoint, or even a heap dump if you're comfortable with that.

This would help confirm whether listeners are accumulating without cleanup — which would explain exactly why memory grows even after connections close.

---

### reporter

I just spent some time going through the logs more carefully:

1. Yes, the 'registering notification listener' messages appear for **every** API request — including simple page loads and task list fetches. Basically any time a user does anything that hits the API, I see one of these log lines. With ~200 users making roughly 25 requests/hour each, that's a lot of listener registrations.

2. I searched through the logs specifically for anything about deregistering, removing, or cleaning up listeners — there's nothing. I only ever see the registration side. No corresponding cleanup message at all. That definitely seems wrong.

3. I don't see a debug endpoint or admin panel option for listing active listeners. I'm not super familiar with doing heap dumps on this process, but I can tell you that based on the log volume, the number of registered listeners must be enormous. If every API request registers one and none are ever removed, we're talking thousands per hour accumulating all day long. That lines up with the memory growth pattern we're seeing on Grafana.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3: notification listeners registered on every API request but never cleaned up","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server process leaks memory steadily throughout the day, growing from ~500MB at startup to 4GB+ by end of day. This causes progressive slowdowns (10+ second page loads) and API timeouts, requiring daily server restarts. The leak is traced to the new 'improved real-time notifications' feature introduced in v2.3.","root_cause_hypothesis":"The v2.3 real-time notifications feature registers a notification listener for every API request — including simple GETs like loading task lists — but never deregisters or cleans them up. With ~200 users making ~25 requests/hour each, approximately 5,000 listeners accumulate per hour with no corresponding removal. These orphaned listeners hold references that prevent garbage collection, causing the linear memory growth observed on Grafana. WebSocket connections themselves are cleaned up properly on disconnect; the leak is in the listener registration layer, not the connection layer.","reproduction_steps":["Deploy TaskFlow v2.3 using the all-in-one deployment on a server with 8GB RAM","Configure with Postgres and default built-in connection pooling","Have users (or simulate load from ~200 users) make normal API requests over several hours","Monitor server process memory via top/htop — observe steady linear growth","Check server logs for 'registering notification listener' messages on every API request","Confirm no corresponding 'deregistering' or cleanup log messages exist","Compare behavior with v2.2 where no such listener registration occurs and memory is stable"],"environment":"Ubuntu 22.04 VM, 8GB RAM, all-in-one TaskFlow deployment, Postgres with default built-in connection pooling, TaskFlow v2.3, ~200 active users","severity":"high","impact":"All 200 active users are affected. Server becomes unusable by late afternoon each day, requiring manual daily restarts. No data loss reported, but significant productivity disruption and operational burden.","recommended_fix":"Investigate the notification listener registration code introduced in v2.3 (likely in the real-time notifications feature). The listener is being registered on every API request handler but lacks a corresponding deregistration/cleanup step. The fix should either: (1) add cleanup logic to remove the listener when the API request completes, (2) reuse a single listener per session/connection rather than per-request, or (3) scope listeners to WebSocket connections only (where real-time notifications are actually delivered) rather than all API requests. Diff the request middleware/handler chain between v2.2 and v2.3 to find the new registration call.","proposed_test_case":"Write a load test that simulates 100+ API requests in sequence, then checks that the number of registered notification listeners does not grow linearly with request count. After requests complete, listener count should return to baseline. Also add a unit test for the notification listener lifecycle ensuring that every register call has a corresponding deregister path.","information_gaps":["Exact code path where the listener is registered (needs source inspection of v2.3 diff)","Whether the leak also occurs in split deployments (API server separate from WebSocket server) or only in all-in-one mode","Precise memory footprint per leaked listener (would help estimate time-to-OOM under different loads)"]}

---
