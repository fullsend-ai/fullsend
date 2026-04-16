# Conversation: memory-leak x omo-prometheus

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana data — that consistent memory climb pattern is very helpful.

To start narrowing this down: **is this a recent change in behavior, or has it always been this way?** Specifically:

- When did you first notice the slowdown? Can you tie it to a particular TaskFlow version upgrade, configuration change, or increase in user count?
- What version of TaskFlow are you running, and what was the previous version if you recently upgraded?

This will help us determine whether we're looking at a regression or a long-standing issue that's now hitting a threshold.

---

### reporter

This definitely started after we upgraded to v2.3 about a week ago. We were on v2.2 before that and never had this issue — the server would run for weeks without needing a restart. We upgraded to v2.3 last Monday and by Tuesday afternoon we noticed the slowdown for the first time. We're running TaskFlow v2.3 on an Ubuntu 22.04 VM with 8GB of RAM. Nothing else changed — same server, same user count, same configuration. It's purely the version upgrade that triggered this.

---

### triage-agent

That's very clear — a clean regression in v2.3, which makes this much easier to track down.

Next I need to understand *where* the memory is going. Can you check the following and share what you find?

1. **Which process is consuming the memory?** When the server is at ~4GB, run `ps aux --sort=-%mem | head -20` — is it the main TaskFlow web server process, a background worker, or something else (e.g. the database)?
2. **Are there any errors or warnings in the TaskFlow logs** (`/var/log/taskflow/` or wherever your logs live) that appear repeatedly or increase in frequency as the day goes on?

This will help us pinpoint whether we're looking at a leak in the web server, a runaway background job, a connection pool issue, or something else entirely.

---

### reporter

I just ran that command and it's definitely the main TaskFlow web server process that's eating the memory — it's sitting at about 3.8GB right now. The background workers and database are using normal amounts of memory.

As for the logs, I don't see any errors or stack traces, but I did notice that there are a LOT of log lines related to the notification system — like WebSocket connection and event registration messages. They seem to just keep accumulating throughout the day. I didn't think much of it at first since the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, but now I'm wondering if that's related.

---

### triage-agent

The notification system is looking like our prime suspect. Here's my working theory: **the v2.3 real-time notification feature is leaking WebSocket connections or event listeners** — connections are being opened but never properly closed when users navigate away or their sessions end, so they pile up throughout the day.

Can you help me validate this? While the server is in its degraded state (high memory), could you check:

- **How many active WebSocket connections does the server have?** You can get a rough count with: `ss -s | grep -i estab` for total connections, or more specifically `ss -tnp | grep <taskflow-port> | wc -l` to count connections to the TaskFlow port. With 200 users, we'd expect a few hundred connections at most — if you're seeing thousands, that confirms the leak.

This will tell us whether connections are accumulating unclosed, which would confirm the theory and give us enough to hand this off to a developer.

---

### reporter

I just ran those commands. The total established connections to the TaskFlow port are around 210-220, which seems about right for our 200 users. So it doesn't look like connections are piling up unclosed.

But here's the thing — even though the connection count looks normal, the memory is still at 3.8GB and those notification log lines I mentioned keep growing. I went back and looked more carefully at the logs, and I'm seeing what looks like event listener registration messages for every single API request — like each time a user loads a page or hits the API, a new listener gets registered on the WebSocket for push notifications. But I don't see corresponding messages for listeners being *removed* or cleaned up. Could it be that the listeners are leaking rather than the connections themselves?

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification system: event listeners registered per API request but never cleaned up","problem":"After upgrading from v2.2 to v2.3, the TaskFlow web server process leaks memory steadily throughout the day, growing from ~500MB at startup to 4GB+ by end of day, causing 10+ second page loads and API timeouts. The server requires a daily restart. This affects all 200 active users on the self-hosted instance.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature registers a new event listener on the WebSocket notification system for every incoming API request (page load, API call, etc.), but never removes or deregisters these listeners. While the WebSocket connections themselves are managed correctly (~210 connections for 200 users), the per-request listeners accumulate unboundedly in memory throughout the day. This is a classic event-listener leak — the connection is reused but listeners pile up on it.","reproduction_steps":["Deploy TaskFlow v2.3 on a server (Ubuntu 22.04, 8GB RAM used in report)","Allow ~200 users to use the application normally throughout a workday","Monitor memory usage of the main TaskFlow web server process over several hours","Observe memory climbing steadily from ~500MB toward 4GB+","Check TaskFlow logs for accumulating event listener registration messages with no corresponding cleanup/removal messages","Verify WebSocket connection count remains normal (~1 per active user), confirming the leak is in listeners, not connections"],"environment":"TaskFlow v2.3, Ubuntu 22.04 VM, 8GB RAM, ~200 active users, self-hosted. Issue does not occur on v2.2.","severity":"high","impact":"All users on any v2.3 instance are affected. Performance degrades progressively throughout the day, becoming unusable (10+ second loads, API timeouts) by late afternoon. Workaround exists (daily server restart) but is disruptive and unsustainable.","recommended_fix":"Examine the v2.3 notification system code (likely introduced or modified in the 'improved real-time notifications' feature). Look for where event listeners are registered in the API request handling path — there should be a listener registration that fires on each incoming request to push real-time updates. Add proper lifecycle management: either (1) check for an existing listener before registering a new one (deduplicate per session/connection), (2) remove listeners when the request completes, or (3) attach listeners at WebSocket connection time rather than per-request. Diffing v2.2 and v2.3 notification system code should pinpoint the exact change that introduced the per-request registration.","proposed_test_case":"Write a test that simulates a single WebSocket connection making N API requests (e.g., 1000) and asserts that the number of registered event listeners on that connection remains bounded (e.g., O(1) rather than O(N)). Additionally, monitor process memory after a sustained load test to verify it remains stable rather than growing linearly with request count.","information_gaps":["Exact log line format for the listener registration messages (would help pinpoint the code path but developer can grep for it)","Whether a configuration flag exists to disable the real-time notification feature (could serve as an interim workaround beyond daily restarts)"]}

---
