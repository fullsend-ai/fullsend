# Conversation: memory-leak x structured-triage

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent memory climb pattern is really helpful.

To help narrow down the cause, could you describe what the typical workload looks like during the day? Specifically:

- Are there particular features your team uses heavily (e.g., file attachments, real-time notifications, recurring task schedules, webhooks/integrations)?
- Does the slowdown correlate with peak usage times, or does it degrade at the same rate even on lighter days?

This will help us identify which subsystem is most likely leaking memory.

---

### reporter

Our team uses TaskFlow pretty broadly — task management, comments, file attachments, the usual. The real-time notifications are definitely heavily used since we have people collaborating throughout the day and relying on push notifications to stay on top of updates.

The degradation rate does seem to correlate with usage. On weekdays with all ~200 users active it climbs faster, and on a quieter Saturday when I was doing maintenance with maybe 20 people online, it climbed much more slowly. The pattern is very linear though — it's not like there's a sudden spike, it just steadily grows.

One thing I should mention — this started about a week ago right after we upgraded from v2.2 to v2.3. I just checked the changelog and v2.3 lists 'improved real-time notifications' as a new feature, which might be relevant given how much we use that. We never had this issue on v2.2.

---

### triage-agent

Thanks for that context — the correlation with the v2.3 upgrade and the real-time notifications feature is a really strong lead.

To round out the environment picture, could you share a few server-side details?

- **OS and runtime:** What OS is the server running, and which Node.js version (or other runtime) are you using?
- **Database:** Which database backend (e.g., PostgreSQL, MySQL, MongoDB) and version?
- **Server resources:** How much total RAM does the host have, and how many CPU cores?

This will help the team reproduce the issue and rule out any environment-specific factors.

---

### reporter

The server is running on Ubuntu 22.04 on a VM with 8GB of RAM. I'm not sure offhand about the exact CPU core count — I'd have to check, maybe 4? As for the runtime and database, I don't recall the specific versions off the top of my head. I'd have to look that up. I can get you those details if you really need them, but I figured the key thing is that this worked perfectly fine on v2.2 with the exact same server and setup — nothing changed except the TaskFlow upgrade to v2.3.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notifications causes progressive server slowdown","problem":"After upgrading from TaskFlow v2.2 to v2.3, the self-hosted server exhibits a steady memory leak — climbing from ~500MB at startup to 4GB+ over the course of a workday. This causes page loads to exceed 10 seconds and API timeouts by late afternoon, requiring a daily server restart. The issue did not exist on v2.2.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature is likely leaking memory — most probably through WebSocket connections or event listeners that are not being properly cleaned up when users disconnect or between notification dispatches. The linear, usage-correlated growth pattern (faster with 200 users, slower with 20) suggests per-connection or per-event resource accumulation.","reproduction_steps":["Deploy TaskFlow v2.3 on a server with ~8GB RAM","Allow normal user activity (~200 active users with real-time notifications enabled)","Monitor memory usage over several hours via Grafana or similar","Observe steady linear memory climb from ~500MB toward 4GB+","Confirm page load times degrade to 10+ seconds and API begins timing out"],"environment":"Ubuntu 22.04, 8GB RAM VM (~4 CPU cores), TaskFlow v2.3, ~200 active users, self-hosted deployment","severity":"high","impact":"All ~200 users on the instance experience progressively degrading performance throughout each workday, with the application becoming effectively unusable by late afternoon. Requires daily manual server restarts to maintain service.","recommended_fix":"Investigate the real-time notifications changes introduced in v2.3. Focus on: (1) WebSocket connection lifecycle — are connections and associated event listeners properly cleaned up on disconnect? (2) In-memory subscriber/listener registries — are entries removed when no longer needed? (3) Notification dispatch buffers — are processed notifications being released? A heap snapshot comparison between v2.2 and v2.3 under load would quickly identify the leaking objects.","proposed_test_case":"Create a load test that simulates repeated WebSocket connect/disconnect cycles and sustained notification delivery over several hours. Assert that memory usage remains within a bounded range (e.g., no more than 20% growth over baseline after reaching steady state) and that no WebSocket handlers or event listeners accumulate beyond the active connection count.","information_gaps":["Exact Node.js runtime version and database backend/version (unlikely to be relevant given the issue is version-regression specific)","Server-side error logs or stack traces during the degraded state","Heap dump or memory profiler output identifying specific leaking objects"]}

---
