# Conversation: memory-leak x structured-triage

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent memory climb pattern is very helpful.

To help narrow down the source of the memory leak, could you walk us through what a typical day of usage looks like? Specifically:

- What are users primarily doing in TaskFlow during the day? (e.g., creating tasks, running reports, using integrations, bulk imports)
- Does the memory climb happen even during low-usage periods (e.g., weekends), or only when users are actively working?

This will help us identify whether the leak is tied to a specific feature or simply correlates with general request volume.

---

### reporter

Our users do a mix of everything — creating and updating tasks, commenting, checking dashboards. Nothing unusual, just normal daily workflow. We don't have heavy bulk imports or anything like that.

On weekends usage drops significantly (maybe 20-30 users instead of 200), and the memory growth is much slower — we can usually get through a weekend without restarting. So it definitely correlates with active usage.

One thing I should mention: this only started after we upgraded to v2.3 about a week ago. We were on v2.2 for months with no issues like this. I noticed the v2.3 changelog mentioned 'improved real-time notifications' as a new feature, which makes me wonder if that's related, since every page in TaskFlow seems to use the notification system.

---

### triage-agent

Thanks for that context — the correlation with the v2.3 upgrade and the real-time notifications feature is a really strong lead.

Could you share a few details about your server environment? Specifically:

- **OS and runtime:** What OS is the server running, and which Node.js (or relevant runtime) version?
- **Database:** Which database are you using (e.g., PostgreSQL, MySQL), and its version?
- **Deployment setup:** Is this a single server, or are you running multiple instances behind a load balancer?

This will help the team reproduce the issue in a matching environment and rule out any runtime-specific causes.

---

### reporter

The server is running Ubuntu 22.04 on a single VM with 8GB of RAM. It's just a single server, no load balancer or anything like that. As for the Node.js version or database specifics, I'd have to go check — I don't have those off the top of my head. I'm the sysadmin so I can look it up if you really need it, but I'm not sure the runtime version is the issue here since it was working perfectly fine on the same setup before we upgraded to v2.3.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 causes progressive server degradation requiring daily restart","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server exhibits a memory leak that causes memory usage to climb from ~500MB at startup to 4GB+ over the course of a business day. This results in page loads exceeding 10 seconds and API timeouts by late afternoon, requiring a daily server restart. The growth rate correlates with active user count (~200 weekday users vs. 20-30 weekend users).","root_cause_hypothesis":"The 'improved real-time notifications' feature introduced in v2.3 is the most likely source of the memory leak. Given that every page in TaskFlow uses the notification system and the leak correlates with active user count, the probable cause is that WebSocket connections or notification event listeners/subscriptions are not being properly cleaned up when users navigate between pages or disconnect. Each active user session likely accumulates orphaned listeners or cached notification state that is never garbage collected.","reproduction_steps":["Deploy TaskFlow v2.3 on a single server (Ubuntu 22.04, 8GB RAM)","Allow ~200 users to perform normal daily workflows (creating tasks, updating tasks, commenting, viewing dashboards)","Monitor server memory usage over 6-8 hours via Grafana or similar","Observe memory climbing steadily from ~500MB toward 4GB+","Confirm page load times degrade to 10+ seconds and API begins timing out"],"environment":"Ubuntu 22.04, single VM with 8GB RAM, self-hosted single-server deployment (no load balancer), TaskFlow v2.3, approximately 200 active weekday users","severity":"high","impact":"All 200 active users experience progressively degraded performance throughout the day, with the application becoming effectively unusable by late afternoon. Requires daily manual server restarts as a workaround, which causes downtime.","recommended_fix":"Investigate the real-time notifications system introduced in v2.3. Specifically: (1) Check whether WebSocket connections or SSE streams are properly closed when clients disconnect or navigate away. (2) Look for event listener or subscription accumulation — verify that per-user notification handlers are removed on disconnect. (3) Review any in-memory caching or state stores added for the notification feature (e.g., notification queues, unread counts) for unbounded growth. (4) Compare the notification module's connection lifecycle management between v2.2 and v2.3. A heap snapshot comparison between a fresh server and one running for several hours under load should confirm the source of retained objects.","proposed_test_case":"Write a load test that simulates 200 concurrent users connecting, performing typical actions (page navigations, task CRUD), and disconnecting/reconnecting over a simulated 8-hour period. Assert that server memory usage remains within a bounded range (e.g., does not exceed 2x baseline) and that no WebSocket connections or event listeners accumulate beyond the active user count. Additionally, a unit test should verify that notification subscriptions are cleaned up when a user session ends.","information_gaps":["Exact Node.js runtime version (reporter indicated same setup as v2.2 which worked fine)","Database type and version","Server-side error logs or warnings that may accompany the memory growth","Whether the Grafana dashboard shows any other correlated metrics (e.g., open connection count, event loop lag)","Whether disabling the real-time notifications feature (if possible) prevents the memory leak"]}

---
