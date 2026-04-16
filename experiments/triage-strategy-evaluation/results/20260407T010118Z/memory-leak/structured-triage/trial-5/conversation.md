# Conversation: memory-leak x structured-triage

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent memory climb pattern is very helpful and strongly suggests a memory leak.

Could you share some details about your server environment? Specifically:
- What version of TaskFlow are you running?
- What OS and runtime is the server on (e.g., Node.js version, Python version)?
- What database are you using (and its version)?
- How is TaskFlow deployed (Docker, bare metal, Kubernetes, etc.)?

---

### reporter

We're running TaskFlow v2.3 — we upgraded from v2.2 about a week ago, which is actually right around when this problem started. The server is on Ubuntu 22.04, it's a VM with 8GB RAM. We deployed it bare metal following the official install guide. As for the database, we're using the default PostgreSQL setup that came with the install, though I'd have to check the exact version. I'm not sure offhand what runtime version we're on either, but I can look that up if it's important.

---

### triage-agent

Thanks for those details — the timing correlation with the v2.3 upgrade is a really important clue.

Could you help me nail down the reproduction pattern? Specifically:
- Can you confirm the memory growth started immediately after upgrading to v2.3, and that you didn't see this behavior on v2.2?
- Does the memory growth happen even during low-usage periods (e.g., overnight or on weekends), or only when users are actively using the system?

This will help us determine whether the leak is tied to the upgrade itself and whether it's triggered by user activity or a background process.

---

### reporter

Yes, I can confirm we never had this issue on v2.2 — the server would run for weeks without needing a restart. The memory growth started right after we upgraded to v2.3. As for the usage pattern, the memory only climbs during business hours when people are actively using the system. On weekends when nobody's logged in, the memory stays flat. Monday morning it's still at whatever level it was at Friday evening. So it's definitely tied to user activity, not a background process.

---

### triage-agent

Thanks for confirming the v2.3 correlation and the user-activity trigger — that narrows things down significantly.

One more thing that would help the team investigate: are there any error messages, warnings, or unusual entries in the TaskFlow server logs during the period when memory is climbing? Even if there are no outright errors, things like repeated warnings, slow query notices, or stack traces around the time performance degrades would be very useful. If the logs are large, entries from the last hour before you typically need to restart would be the most relevant.

---

### reporter

I haven't noticed any outright errors in the logs, but I haven't done a deep dive either. Let me check... Looking at the logs from yesterday afternoon before we restarted, I do see a lot of entries related to the notification system — things like WebSocket connection messages and event listener registrations. There seem to be a *lot* of them, way more than I'd expect. No error messages or stack traces though, and no slow query warnings from the database side. It's mostly just a high volume of notification-related log entries. I can pull some specific log snippets if that would help.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3: notification/WebSocket event listeners accumulate during user activity","problem":"After upgrading from TaskFlow v2.2 to v2.3, the server's memory usage climbs steadily from ~500MB to 4GB+ during business hours, causing progressive slowdown (10+ second page loads, API timeouts) and requiring daily restarts. The issue did not exist on v2.2.","root_cause_hypothesis":"The v2.3 upgrade introduced a leak in the notification subsystem — likely WebSocket connection handlers or event listeners that are registered per user activity but never cleaned up. The high volume of WebSocket connection messages and event listener registration log entries supports this. Each user interaction likely adds listeners that are not removed when connections close or users navigate away.","reproduction_steps":["Deploy TaskFlow v2.3 on a server (bare metal, Ubuntu 22.04)","Allow ~200 users to use the system normally during business hours","Monitor memory usage via Grafana or similar — memory will climb steadily","After several hours, page loads degrade to 10+ seconds and API calls time out","Restarting the server resets memory to ~500MB"],"environment":"TaskFlow v2.3 (upgraded from v2.2), Ubuntu 22.04 VM with 8GB RAM, bare metal deployment, PostgreSQL (default install), ~200 active users","severity":"high","impact":"All 200 users experience progressively degraded performance throughout each business day. The team must restart the server daily, causing a maintenance burden and potential disruption.","recommended_fix":"Diff the notification/WebSocket subsystem between v2.2 and v2.3 to identify changes. Look specifically for event listener registrations that lack corresponding cleanup (removeListener/off calls) on WebSocket disconnect or session end. A heap snapshot comparison between startup and after several hours of use would confirm the leaking objects. Check for patterns like registering listeners inside request handlers without removing them.","proposed_test_case":"Simulate repeated WebSocket connections and disconnections (e.g., 100 connect/disconnect cycles). Assert that the count of registered event listeners and memory usage return to baseline after all connections close. This can be a load test or unit test against the notification module.","information_gaps":["Exact PostgreSQL and runtime (Node.js/Python) versions — unlikely to be the root cause given the v2.3 correlation","Specific log snippets showing the WebSocket/event listener entries — would confirm the pattern but the description is sufficient for investigation","Whether the v2.3 changelog mentions notification system changes"]}

---
