# Conversation: memory-leak x superpowers-brainstorming

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that steady memory climb is a classic memory leak pattern.

To help narrow this down: which version of TaskFlow are you running, and how is it deployed?

a) Docker container (if so, which image tag?)
b) Direct Node.js process (e.g. `node server.js` or via pm2/systemd)
c) Kubernetes or other orchestrator
d) Something else

Also, has this always happened, or did it start after a specific upgrade or configuration change?

---

### reporter

We're running TaskFlow v2.3, which we upgraded to about a week ago — that's actually right when this problem started, now that you mention it. We were on v2.2 before and didn't have this issue. The server runs as a systemd service on an Ubuntu 22.04 VM with 8GB RAM. Not containerized or anything like that, just a straightforward deployment. I noticed the v2.3 changelog mentioned 'improved real-time notifications' as one of the new features, not sure if that's relevant but figured I'd mention it.

---

### triage-agent

Thanks, the timing lines up perfectly — the leak started right when you upgraded to v2.3, and the 'improved real-time notifications' feature is a strong suspect. Real-time features (WebSockets, SSE, etc.) are a common source of memory leaks when connections or event listeners aren't cleaned up properly.

Before I write up the triage summary, one quick question: are your users actively using real-time notifications (e.g., do they see live updates for task changes without refreshing the page)?

a) Yes, we see real-time updates working in the UI
b) Not sure — we haven't specifically noticed or configured it
c) We actually disabled that feature / it doesn't seem to be active
d) Something else

---

### reporter

Yes, real-time notifications are definitely active — our users see live updates when tasks are assigned to them or when someone comments on a task they're watching. It actually works well from a feature perspective, the updates pop up without needing to refresh. That was one of the things we were excited about in the v2.3 upgrade. It's just the memory issue that's killing us.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notification system causes progressive slowdown and daily restart requirement","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory steadily throughout the day, climbing from ~500MB at startup to 4GB+ by end of day. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, forcing a daily restart. The issue affects all 200 active users on the instance.","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 is likely leaking WebSocket/SSE connections or event listeners. With 200 active users generating connection events throughout the day, connection handlers or notification event listeners are probably being created but not properly cleaned up on disconnect, tab close, or session timeout. This is consistent with the steady linear memory growth pattern visible in Grafana.","reproduction_steps":["Deploy TaskFlow v2.3 as a Node.js process (systemd service)","Allow active users to connect and use real-time notifications over the course of several hours","Monitor memory usage via process metrics or Grafana","Observe steady memory climb (~3.5GB growth over a full workday with ~200 users)","Compare with v2.2 under identical conditions to confirm the regression"],"environment":"TaskFlow v2.3, Node.js, systemd service on Ubuntu 22.04 VM with 8GB RAM, ~200 active users, upgraded from v2.2 (which did not exhibit the issue)","severity":"high","impact":"All 200 users experience progressively degrading performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires daily manual restart to maintain service.","recommended_fix":"Investigate the real-time notification code introduced or changed in v2.3. Specifically look for: (1) WebSocket or SSE connections not being closed/cleaned up on client disconnect, (2) event listeners accumulating without removal (check for Node EventEmitter 'MaxListenersExceededWarning'), (3) in-memory data structures (subscriber maps, notification queues) that grow per-connection but are never pruned. A heap snapshot comparison between startup and after several hours of use should pinpoint the leaking objects. As a short-term mitigation, consider whether connection idle timeouts or periodic cleanup sweeps can be added.","proposed_test_case":"Write a load test that simulates 50+ concurrent users opening and closing real-time notification connections over a 1-hour period. Assert that server memory usage remains within a bounded range (e.g., does not grow beyond 200MB above baseline) and that closed connections are fully garbage-collected. Also add a unit test verifying that notification event listeners are removed when a client disconnects.","information_gaps":["Exact Node.js version and whether the --max-old-space-size flag is set","Whether the Grafana dashboard shows any correlation between active WebSocket connection count and memory growth","Server-side logs showing any warnings (e.g., EventEmitter memory leak warnings)"]}

---
