# Conversation: memory-leak x socratic-refinement

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — that consistent memory climb is really helpful to see.

To help narrow down what's driving this, I'm curious about the shape of your team's usage throughout the day. Does the memory growth seem to track with user activity (e.g., does it climb faster during peak hours and plateau on weekends/off-hours), or does it seem to grow at a steady rate regardless of how many people are using it? And have you noticed whether any particular workflows — like bulk task imports, report generation, real-time collaboration, or webhook integrations — tend to be happening when things get especially sluggish?

---

### reporter

Good question. The memory growth definitely tracks with user activity. During business hours (roughly 9-5) when all ~200 users are active, the memory climbs steadily. On weekends when almost nobody is on, the server barely grows at all — we actually skip the restart on weekends. So it's clearly tied to usage volume.

As for specific workflows, I haven't been able to pin it to one particular action. It seems like normal everyday usage drives it — people viewing tasks, updating statuses, adding comments, the usual. We don't do a lot of bulk imports or heavy report generation. One thing I can mention is that this all started about a week ago when we upgraded from v2.2 to v2.3. We were rock solid on v2.2 for months before that. I noticed the v2.3 changelog mentioned 'improved real-time notifications' among other things, not sure if that's relevant.

---

### triage-agent

That's a really valuable detail about the v2.3 upgrade timing — that strongly suggests a regression in the new release.

I'd like to understand what happens at the boundary between active and inactive periods. When your users log off for the evening (but before you restart), does the memory usage stay flat at whatever level it reached, or does it slowly drop back down at all? In other words, once the memory climbs to 4GB, does it ever reclaim any of that on its own, even partially?

Also, are you able to check how many active connections (WebSocket or otherwise) the server is holding at any given time — say, comparing a count in the morning vs. late afternoon? I'm wondering if the new real-time notifications feature might be accumulating connections or subscriber state that isn't being released when users close their browsers or navigate away.

---

### reporter

Once everyone logs off for the evening, the memory stays right where it is — it doesn't drop back down at all. If it hit 3.5GB by 5pm, it's still sitting at 3.5GB at 10pm. The only thing that brings it back down is a full server restart. So whatever is accumulating, the server never cleans it up on its own.

As for connection counts, I haven't specifically tracked WebSocket connections, but that's a good idea — I can check that. I do know from our Grafana metrics that active HTTP connections drop way down in the evening as expected when people leave, so the normal request handling seems to wind down fine. But if there's something on the notification side that's hanging around, I wouldn't have been monitoring that separately. I can try to look into it tomorrow if you can point me at what to check.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in v2.3 real-time notifications causes progressive server degradation requiring daily restart","problem":"After upgrading from v2.2 to v2.3, the TaskFlow server leaks memory proportional to user activity, climbing from ~500MB to 4GB+ during a business day. Memory is never reclaimed even after users disconnect. The server becomes unusably slow (10s+ page loads, API timeouts) and requires a daily restart.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature is likely accumulating per-session state (event listeners, subscription objects, or notification buffers) that is not released when users disconnect or navigate away. This explains why memory grows with user activity, never shrinks after hours, and was not present in v2.2.","reproduction_steps":["Deploy TaskFlow v2.3 on a self-hosted instance","Allow ~200 users to perform normal operations (viewing tasks, updating statuses, adding comments) over a business day","Monitor memory usage — it will climb steadily during active hours","After users disconnect, observe that memory remains at peak and is not reclaimed","Compare with v2.2 under the same usage pattern, which does not exhibit the leak"],"environment":"Self-hosted TaskFlow v2.3 (upgraded from v2.2 ~1 week ago), ~200 active users, monitored via Grafana","severity":"high","impact":"All 200 users experience progressively degraded performance throughout the day, with the application becoming effectively unusable by late afternoon. Operations team must restart the server daily.","recommended_fix":"Diff the real-time notification system between v2.2 and v2.3. Look for per-connection or per-session state (subscriber lists, event emitters, notification queues, WebSocket handlers) that is allocated on user connect/activity but never freed on disconnect. Likely candidates: event listener registrations without corresponding removal, in-memory notification buffers that grow unboundedly, or WebSocket connection objects retained after the socket closes. Add proper cleanup on disconnect/session-end and consider bounded data structures for any per-user notification state.","proposed_test_case":"Write a load test that simulates users connecting, performing actions, and disconnecting in cycles. After a full cycle of connect-use-disconnect, assert that memory returns to baseline (within a reasonable margin). Specifically test that real-time notification subscriptions are cleaned up by checking subscriber counts before and after user disconnect.","information_gaps":["Exact WebSocket connection count comparison (morning vs afternoon vs after-hours) — would confirm the hypothesis but doesn't change the investigation direction","Whether the leak is per-connection, per-event, or per-notification (requires code inspection)","Full v2.3 changelog beyond 'improved real-time notifications' — other changes could also contribute"]}

---
