# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent memory climb is really helpful to see.

I'd like to understand the timeline a bit better: you mentioned you've been doing daily restarts for the past week. Was there anything that changed around the time this started — a TaskFlow upgrade, a configuration change, a jump in user count, or maybe a new integration or workflow your team started using? Even something that seemed unrelated at the time could be a useful clue.

---

### reporter

Yes, actually — we upgraded from TaskFlow v2.2 to v2.3 about a week ago, which lines up pretty much exactly with when this started. No other changes on our end: same user count, same server, same configuration. I remember the v2.3 changelog mentioned something about 'improved real-time notifications' as one of the new features. I didn't think much of it at the time, but the timing is suspicious now that you mention it.

---

### triage-agent

That timing is really telling — the v2.3 upgrade lines up perfectly. The real-time notifications feature is a great thread to pull on, since features like that typically use persistent connections (like WebSockets) that can leak memory if they aren't cleaned up properly.

Could you tell me a bit more about what you observe with those notifications during the day? For example: do your users keep TaskFlow open in browser tabs for long stretches, and have you noticed whether the notification behavior itself changes as the server slows down (e.g., duplicate notifications, notifications stopping, or connections dropping and reconnecting)? Also, if you're able to check — does the number of active connections or threads on the server grow steadily through the day the same way the memory does?

---

### reporter

Good question. Yes, most of our users do keep TaskFlow open in their browsers throughout the workday. I haven't heard specific complaints about duplicate notifications, but a few users have mentioned that notifications seem to stop arriving later in the day — which tracks with when the server starts getting sluggish.

As for connections and threads — I do have some server-level monitoring in Grafana. The number of active WebSocket connections stays roughly proportional to connected users and looks normal, but I did notice that the process's thread count and open file descriptors grow steadily throughout the day in a pattern that looks a lot like the memory graph. It doesn't plateau — it just keeps climbing as long as the server is up. The growth seems to correlate more with overall request volume than with the number of connected users, if that makes sense. During lunch when traffic dips, the growth slows but never reverses.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification system: per-request resource leak causes threads and file descriptors to grow unbounded","problem":"After upgrading from TaskFlow v2.2 to v2.3, the server exhibits a steady memory leak — climbing from ~500MB at startup to 4GB+ over the course of a workday — causing progressive slowdown (10+ second page loads, API timeouts) and requiring daily restarts. The leak correlates with request volume and never reverses, even during low-traffic periods.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature is likely registering event listeners, notification handlers, or thread-local resources on every incoming request without releasing them. This explains why thread count and open file descriptors mirror the memory growth curve and correlate with request volume rather than connected user count. The WebSocket connections themselves are managed correctly (proportional to users), but the per-request code path that interacts with the notification system is leaking resources.","reproduction_steps":["Deploy TaskFlow v2.3 with real-time notifications enabled (default configuration)","Simulate sustained request traffic from multiple users (200 concurrent users keeping the app open)","Monitor process memory, thread count, and open file descriptor count over several hours","Observe that all three metrics grow steadily with cumulative request volume and never decrease","Compare against v2.2 under identical load to confirm the regression"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2 ~1 week ago), ~200 active users, monitored via Grafana. No other infrastructure or configuration changes coincided with the upgrade.","severity":"high","impact":"All 200 users experience progressively degraded performance throughout the workday, with the application becoming effectively unusable by late afternoon. Requires manual daily restart as a workaround, causing downtime and operational burden.","recommended_fix":"Investigate the v2.3 real-time notification code path for per-request resource leaks. Specifically: (1) Check whether event listeners or notification subscriptions are being attached on each HTTP request without cleanup. (2) Look for thread or goroutine spawning tied to request handling in the notification system. (3) Check for file descriptor leaks from notification channel setup/teardown. Diff the v2.3 notification changes against v2.2 to isolate what's new. A heap dump or thread dump from a long-running instance would pinpoint the exact objects accumulating.","proposed_test_case":"Run a load test that sends 10,000 sequential requests to the server with real-time notifications enabled, then verify that thread count, file descriptor count, and heap size return to baseline (or remain bounded) after requests complete. The test should fail if any of these metrics show unbounded linear growth proportional to request count.","information_gaps":["Exact server runtime and version (e.g., Node.js, Java, Go) — useful for choosing profiling tools but does not change the investigation target","Whether the reporter has tried rolling back to v2.2 to confirm the regression — strongly suspected but not verified","Whether specific API endpoints or page types contribute disproportionately to the leak"]}

---
