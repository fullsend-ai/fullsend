# Conversation: memory-leak x omo-prometheus

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent daily memory climb is a clear signal of a leak.

To start narrowing this down: **is this a recent change in behavior, or has it always been like this?** Specifically:

- What version of TaskFlow are you running, and did you recently upgrade?
- If this started recently, can you pinpoint roughly when — did it coincide with a version update, a config change, or a change in usage patterns (e.g., more users, new integrations)?

This will help us determine whether we're looking at a regression or a long-standing issue that's surfaced due to scale.

---

### reporter

Yes, this is definitely a recent change. We upgraded from v2.2 to v2.3 about a week ago, and the problems started right after that. Before the upgrade, the server would run for weeks without any issues — memory stayed stable around 500-600MB. We haven't changed anything else: same number of users, same configuration, no new integrations. I did notice that the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, which makes me wonder if that's related, but I'm not sure.

---

### triage-agent

Thanks — that's very helpful. The timing with the v2.3 upgrade and the 'improved real-time notifications' feature is a strong lead.

To dig into this further: **what do your server logs show during the degradation period?** Specifically:

- Are there any errors or warnings accumulating in the TaskFlow logs as memory climbs (e.g., connection errors, timeout warnings, or anything unusual)?
- Can you check the number of active WebSocket or long-lived HTTP connections on the server? For example, run `ss -s` or `netstat -an | grep ESTABLISHED | wc -l` during morning (when it's fast) vs. late afternoon (when it's slow) and compare the counts?

This will help us determine whether the real-time notifications system is leaking connections or accumulating state.

---

### reporter

I haven't dug deeply into the TaskFlow application logs themselves, but I did notice there aren't any obvious errors or crashes — the server just gets progressively slower without any clear error messages.

I went ahead and checked the connection counts like you suggested. In the morning shortly after restart, I see a normal number of established connections — maybe around 200-300, which makes sense for our user count. But by late afternoon, the number of WebSocket connections is way higher than I'd expect. It looks like connections or listeners are piling up over the course of the day rather than being cleaned up. I'm not sure of the exact count but it's noticeably much higher than the morning.

This does seem consistent with the real-time notifications feature being the culprit — it's like every request is adding something that never gets removed.

---

### triage-agent

The picture is coming together — this looks like a connection or listener leak in the v2.3 real-time notifications feature. Every user interaction appears to register something (a WebSocket connection, an event listener, or a subscription) that never gets torn down.

To confirm this theory: **does TaskFlow have a configuration option to disable real-time notifications?** If so, can you try disabling it and running for a day to see if the memory stays stable?

If there's no toggle for that, an alternative test: can you get the exact WebSocket connection count late in the day (e.g., `ss -s` or `ss | grep websocket | wc -l`)? You mentioned it's 'noticeably much higher' — the specific number relative to your ~200 users would tell us whether connections are simply not closing (e.g., 2,000+ when you'd expect ~200) or whether each user session is spawning multiple leaked listeners.

---

### reporter

I checked and I don't see a configuration option to fully disable real-time notifications in the TaskFlow settings — it seems like it's baked into the v2.3 update without a toggle.

I did run `ss | grep websocket | wc -l` late yesterday afternoon, and the count was in the thousands — I think it was around 4,000-5,000. That's way more than our ~200 users would account for. It really does seem like every API request is registering some kind of listener or subscription that never gets cleaned up, not just one per user session. Our users are pretty active — probably around 25 requests per person per hour — so the numbers add up if each request is leaking something.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notifications: WebSocket connections/listeners accumulate per request, never cleaned up","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory throughout the day, climbing from ~500MB at startup to 4GB+ by end of day, causing page loads of 10+ seconds and API timeouts. The server requires a daily restart. The issue affects all ~200 active users on this self-hosted instance.","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 registers a WebSocket connection, event listener, or subscription on every API request rather than once per user session, and never tears them down. This causes linear accumulation proportional to total request volume (~200 users × ~25 requests/user/hour), explaining both the connection count (4,000-5,000 by end of day) and the memory growth.","reproduction_steps":["Deploy TaskFlow v2.3 (upgrade from v2.2 or fresh install)","Run with a meaningful number of concurrent users (reported with ~200)","Monitor memory usage and WebSocket connection count over several hours","Observe memory climbing linearly and WebSocket connections accumulating far beyond the active user count","Compare to v2.2 where memory remains stable at ~500-600MB indefinitely"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2), ~200 active users, ~25 requests per user per hour. Monitoring via Grafana. Exact OS/runtime not specified but issue is in application-level connection management, not infrastructure.","severity":"high","impact":"All ~200 users on the instance experience progressively degrading performance throughout the day, culminating in 10+ second page loads and API timeouts by late afternoon. Requires daily manual restart as a workaround. No data loss reported, but productivity impact is significant.","recommended_fix":"Investigate the real-time notifications code path introduced in v2.3. Look for WebSocket connection or event listener/subscription registration that occurs per-request rather than per-session. The fix likely involves: (1) ensuring notification subscriptions are created once per user session and reused, not per API request; (2) adding proper cleanup/teardown when sessions end or connections close; (3) adding a configuration toggle to disable real-time notifications as a mitigation option. Regression test against v2.2 behavior where memory stayed stable.","proposed_test_case":"Create an automated test that simulates a single user session making 100+ API requests, then verifies that the number of active WebSocket connections/event listeners remains constant (1 per session) rather than growing with request count. Additionally, a soak test with multiple simulated users over an extended period should confirm memory remains bounded.","information_gaps":["Exact server runtime environment (Node.js version, OS, container vs bare metal) — useful for reproducing but not blocking","Whether rolling back to v2.2 fully resolves the issue (strongly expected based on timeline but not explicitly tested)","Exact nature of the leaked resource (WebSocket connection vs event emitter listener vs pub/sub subscription) — developer will identify from v2.3 diff"]}

---
