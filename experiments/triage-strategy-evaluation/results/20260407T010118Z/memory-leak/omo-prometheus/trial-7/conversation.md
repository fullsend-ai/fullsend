# Conversation: memory-leak x omo-prometheus

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily memory climb pattern is very helpful and strongly suggests a memory leak.

To start narrowing this down: which specific process is consuming the growing memory? For example, is it the main TaskFlow application server (Node/Java/Python — which runtime and version are you running?), the database, a background worker, or something else? And what version of TaskFlow are you on — did this behavior start with a particular upgrade, or has it always been this way on your instance?

---

### reporter

It's the main TaskFlow application server process that's growing — we're running TaskFlow v2.3 on Go (whatever runtime version ships with that release). The database and other services look stable in Grafana.

This definitely started with the v2.3 upgrade. We upgraded about a week ago and that's exactly when the daily restarts became necessary. We were on v2.2 before and never had this issue. I actually noticed the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, and I've been wondering if that's related since the timing lines up perfectly.

---

### triage-agent

That's a really strong correlation with the v2.3 upgrade — the real-time notifications feature is a prime suspect.

Can you check one thing for me? Real-time notification systems typically use persistent connections (WebSockets or Server-Sent Events). If you run `netstat -an | grep <taskflow-port> | grep ESTABLISHED | wc -l` (or equivalent) on your server, how many established connections do you see during the morning vs. late afternoon when things are slow? And does your Grafana setup track goroutine count for the TaskFlow process — if so, does that climb throughout the day too?

This will tell us whether connections are accumulating (leak in the notification subsystem) vs. memory growing from something else entirely.

---

### reporter

I just checked the netstat numbers. The established connection count is actually pretty stable — around 180-210 connections, which makes sense for our ~200 users. It doesn't climb significantly throughout the day. So it doesn't look like connections themselves are accumulating.

We do have goroutine count in Grafana actually, and yes, it does climb steadily throughout the day, very similar to the memory graph. It starts around a few hundred at boot and just keeps going up. It doesn't plateau — it's a pretty linear climb that correlates with how busy the server is during working hours.

So it seems like something is being spawned or registered per-request that never gets cleaned up, rather than connections piling up. Given that the notification system is new in v2.3, I'm guessing each request is maybe registering something with that system that doesn't get torn down?

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3: goroutines accumulate per-request in real-time notification subsystem, never cleaned up","problem":"The TaskFlow application server leaks memory from ~500MB to 4GB+ over a working day, causing 10+ second page loads and API timeouts by late afternoon. This requires a daily server restart and affects all 200 users. The issue began immediately after upgrading from v2.2 to v2.3.","root_cause_hypothesis":"The real-time notifications feature introduced in v2.3 spawns a goroutine (or registers a callback/listener) on each incoming request that is never torn down. This causes goroutine count and memory to grow linearly with request volume throughout the day. The stable established connection count (~180-210) rules out a connection leak — the leak is internal to the request-handling path, likely a goroutine per notification dispatch or subscription registration that outlives the request lifecycle.","reproduction_steps":["Deploy TaskFlow v2.3 with real-time notifications enabled (default)","Drive normal user traffic (~200 active users or simulate equivalent load)","Monitor goroutine count and memory via Grafana or Go pprof over several hours","Observe goroutine count climbing linearly with no plateau, and memory growing from ~500MB toward 4GB+","Compare with TaskFlow v2.2 under the same load — no leak observed"],"environment":"Self-hosted TaskFlow v2.3, Go runtime (version bundled with v2.3 release), ~200 active users, previously stable on v2.2","severity":"high","impact":"All 200 users experience progressively degrading performance throughout each day, with the application becoming effectively unusable by late afternoon. Requires daily manual restart as a workaround.","recommended_fix":"Examine the real-time notification code path introduced in v2.3 for goroutines spawned per-request that are not cancelled or cleaned up when the request completes. Likely candidates: (1) a goroutine spawned to push notification updates per request that blocks on a channel forever, (2) a listener/subscriber registered in a notification hub that is never deregistered, or (3) a context-derived goroutine where the context is never cancelled. A Go pprof goroutine dump (`/debug/pprof/goroutine?debug=1`) on a running instance late in the day will immediately reveal which function is accumulating goroutines. As a quick validation, disabling the real-time notification feature (if a feature flag exists) should stop the leak entirely.","proposed_test_case":"Write a load test that simulates a day of typical traffic (e.g., 1000+ requests over a compressed timeframe) and asserts that goroutine count returns to baseline after requests complete. Goroutine count at the end of the test should be within a small delta of the count at the start — any sustained linear growth indicates the leak is still present.","information_gaps":["Exact Go pprof goroutine dump showing which function is accumulating (would confirm the specific code path but developer can obtain this themselves)","Whether a feature flag exists to disable real-time notifications for quick validation","Exact Go runtime version shipped with TaskFlow v2.3"]}

---
