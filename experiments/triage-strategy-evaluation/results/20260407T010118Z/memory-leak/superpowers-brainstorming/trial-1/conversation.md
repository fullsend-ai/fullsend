# Conversation: memory-leak x superpowers-brainstorming

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent memory growth pattern is very helpful.

You mentioned this has been happening for the past week. Did something change around when it started?

- A) We upgraded TaskFlow to a new version
- B) Our user count or usage pattern changed significantly
- C) We changed infrastructure (server, database, reverse proxy, etc.)
- D) Nothing obvious changed — it just started happening
- E) Something else

This will help narrow down whether we're looking at a regression or a latent issue that crossed a threshold.

---

### reporter

A) We upgraded TaskFlow to a new version. We went from v2.2 to v2.3 about a week ago, which lines up exactly with when the problems started. I actually checked the changelog and noticed v2.3 mentions 'improved real-time notifications' as one of the new features — not sure if that's related but figured it's worth mentioning.

---

### triage-agent

That's a strong lead — v2.3's real-time notifications feature is a likely culprit. Memory leaks in notification systems are often caused by WebSocket connections or event listeners not being cleaned up properly.

To help confirm this: does the memory growth rate seem to track with how many users are active?

- A) Yes — on weekends or low-usage periods the growth is slower or absent
- B) No — it grows at roughly the same rate regardless of user activity
- C) We haven't compared, but I can check
- D) Not sure

This will help the team know whether to focus on per-connection leaks vs. a background process issue.

---

### reporter

A) Yes — on weekends or low-usage periods the growth is slower or absent. We actually noticed this over the weekend when only a handful of people were working. The memory barely climbed at all. During normal weekdays with all ~200 users active, it's that steady climb to 3-4GB by end of day. So it definitely correlates with user activity.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notifications causes progressive server slowdown requiring daily restart","problem":"After upgrading from v2.2 to v2.3, the self-hosted TaskFlow instance leaks memory steadily throughout the day, climbing from ~500MB at startup to 4GB+ by end of day with ~200 active users. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, requiring a daily server restart.","root_cause_hypothesis":"The real-time notifications feature introduced in v2.3 is leaking per-connection resources — most likely WebSocket connections, event listeners, or subscription objects that are not being cleaned up when users disconnect, navigate away, or let sessions go idle. The strong correlation between active user count and memory growth rate points to a per-connection leak rather than a background process issue.","reproduction_steps":["Deploy TaskFlow v2.3 (upgrade from v2.2)","Allow ~200 users to use the system normally over a workday","Monitor memory usage over 6-8 hours","Observe steady memory growth correlated with active user count","For faster reproduction: simulate many concurrent users connecting and disconnecting to isolate whether cleanup on disconnect is the issue"],"environment":"Self-hosted TaskFlow v2.3 instance, ~200 active users on weekdays, memory monitored via Grafana","severity":"high","impact":"All ~200 users experience degraded performance by mid-afternoon and near-unusable service by end of day. Requires daily manual restarts, risking data loss or service disruption.","recommended_fix":"Investigate the real-time notification system added in v2.3 for resource leaks. Specifically: (1) Check that WebSocket or SSE connections are properly closed and dereferenced on client disconnect/timeout. (2) Verify event listeners and notification subscriptions are removed when sessions end. (3) Look for growing collections (maps, arrays, sets) that accumulate per-user entries without eviction. A heap snapshot comparison between startup and after several hours of use under load should pinpoint the leaking objects.","proposed_test_case":"Write a load test that simulates 200 users connecting, performing typical actions, and disconnecting repeatedly over a simulated workday. Assert that memory usage after all users disconnect returns to within a reasonable margin of the baseline (e.g., <20% above startup memory). Also test that individual user disconnect properly frees all associated notification resources.","information_gaps":["Exact server runtime and version (Node.js, Java, etc.) — but the dev team already knows this","Whether the notification system uses WebSockets, SSE, or polling — discoverable from v2.3 source","Whether a rollback to v2.2 fully resolves the issue — likely yes given the timing, but not explicitly confirmed"]}

---
