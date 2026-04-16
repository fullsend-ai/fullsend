# Conversation: memory-leak x omc-deep-interview

## Issue

**TaskFlow server gets slower throughout the day, requires daily restart**

Our self-hosted TaskFlow instance starts the day running fine but gets progressively slower. By late afternoon, page loads take 10+ seconds and the API times out. We've been restarting the server every evening for the past week. Memory usage on the server climbs from ~500MB at startup to 4GB+ by end of day. We have about 200 active users.

I've attached a screenshot of our Grafana dashboard showing the memory climb. The pattern is very consistent day-to-day.

---

## Comments

### triage-agent

Thanks for the detailed report and the Grafana dashboard — the consistent daily pattern is really helpful.

To help us narrow down what's consuming that memory: what version of TaskFlow are you running, and how is it deployed? Specifically, are you using any background job processing (e.g., scheduled reports, email notifications, webhook integrations), and have you noticed whether the slowdown correlates with any particular type of activity — for example, does it get worse faster on days with heavier usage?

---

### reporter

We're running TaskFlow v2.3 — we upgraded from v2.2 about a week ago, which is actually right around when this problem started. Good catch, I hadn't connected those dots explicitly but the timing lines up perfectly. It's deployed on an Ubuntu 22.04 VM with 8GB RAM.

We do use the real-time notification system and we have some webhook integrations set up. I haven't noticed it being worse on specific days, but it does seem to correlate with overall usage — during a holiday last week when fewer people were on, the memory climbed slower. With our normal ~200 users it's very consistent though, hitting 3-4GB by end of day.

I just checked the v2.3 changelog and one of the features listed is 'improved real-time notifications' — could that be related?

---

### triage-agent

That timing with the v2.3 upgrade is a really strong clue — definitely worth investigating. Two things that would help us isolate this:

Could you check how many active WebSocket connections the server is holding? On most TaskFlow deployments you can see this at the `/admin/status` endpoint or by running `ss -s` on the server during the afternoon when memory is high. I'm wondering whether the new real-time notification system might be leaking connections — if the connection count climbs in step with memory, that would strongly point to the notification layer.

Also, as a quick sanity check: did you happen to change anything else around the same time as the upgrade — new webhook integrations, a bump in user count, or infrastructure changes? I want to make sure we're not anchoring too hard on v2.3 if something else shifted at the same time.

---

### reporter

No, nothing else changed around the same time — no new webhook integrations, same user count, same VM. The v2.3 upgrade was the only change that week, so I'm fairly confident it's the trigger.

I haven't checked the WebSocket connection count specifically, but I'll take a look at `/admin/status` and run `ss -s` tomorrow afternoon when the memory is high and report back. That's a great idea. I will say that from the Grafana metrics, the memory growth looks very linear and steady throughout the day — it's not like there are sudden jumps. It just climbs consistently as people use the system, roughly tracking with request volume. On that holiday with fewer users, the slope was noticeably flatter, which does seem consistent with something leaking per-request rather than per-connection.

---

### triage-agent

That observation about linear growth tracking request volume is really useful — it actually makes me reconsider the WebSocket connection angle. A connection leak would more likely show step-wise growth as users log in, whereas what you're describing sounds more like something is being allocated on every request and never released.

When you check `/admin/status` tomorrow, in addition to WebSocket connections, could you also look at whether there's a count of active notification subscriptions or event listeners? And one more thing that would really help us nail this down: is there a way for you to temporarily disable the real-time notification feature (some deployments have a `TASKFLOW_REALTIME_ENABLED=false` flag or similar) and see if the memory growth stops? Even running that way for a few hours would tell us definitively whether the notification system is the culprit versus something else that changed in v2.3.

---

### reporter

That's a really smart way to think about it — per-request rather than per-connection. That matches what I'm seeing too, since the growth rate seems proportional to how many people are actively using the system, not just how many are logged in.

I'll definitely check `/admin/status` for notification subscriptions and event listener counts tomorrow. I'd expect with ~200 users making maybe 25 requests per hour each, if something is leaking per-request, that's a lot of accumulation over an 8-hour workday.

As for disabling real-time notifications — I just checked and we do have the `TASKFLOW_REALTIME_ENABLED` config option available. I can try setting that to false tomorrow morning and monitor throughout the day. Our users will miss the push notifications but they can live without them for a day if it helps us isolate the issue. I'll report back with both the `/admin/status` data and the results of disabling real-time notifications.

---

### triage-agent

Great plan — the toggle test and `/admin/status` check should tell us a lot. One more thing that would really accelerate the fix once a developer picks this up: when memory is high tomorrow afternoon, could you also grab a heap dump from the Node process? You can trigger one by running:

```
kill -USR2 $(pgrep -f taskflow)
```

or if TaskFlow is running with `--inspect`, you can connect Chrome DevTools to `chrome://inspect` and take a heap snapshot from there. The resulting file will show exactly which objects are accumulating in memory, which would let a developer go straight to the leaking code path rather than having to reproduce and profile from scratch. Even if you're not sure how to read it, just attaching the file to this issue would be incredibly helpful.

---

### reporter

Sure, I can try to grab a heap dump tomorrow afternoon when memory is high. I'm more of a sysadmin than a developer so I haven't done that before, but the `kill -USR2` approach looks straightforward enough. I'll give that a shot and attach whatever file it produces to this issue.

So the plan for tomorrow is: disable real-time notifications in the morning, monitor memory throughout the day, and if the leak still happens (or on a second day with notifications re-enabled), check `/admin/status` for listener/subscription counts and grab a heap dump in the afternoon. I'll report back with everything I find.

---

### triage-agent

[RESOLVED] {"title":"Memory leak in TaskFlow v2.3 causes progressive slowdown — likely per-request allocation in real-time notification system","problem":"Self-hosted TaskFlow instance exhibits a steady memory leak, climbing from ~500MB at startup to 4GB+ over an 8-hour workday with ~200 active users. This causes page loads exceeding 10 seconds and API timeouts by late afternoon, requiring a daily server restart. The issue began immediately after upgrading from v2.2 to v2.3.","root_cause_hypothesis":"The v2.3 'improved real-time notifications' feature is the most likely culprit. Memory growth is linear and proportional to request volume (not connection count), suggesting something is being allocated on every request within the notification code path and never released — e.g., event listeners, subscription objects, or notification payloads accumulating in memory per-request rather than being garbage collected.","reproduction_steps":["Deploy TaskFlow v2.3 on a server with real-time notifications enabled (default)","Simulate normal usage with ~200 concurrent users making ~25 requests/hour each","Monitor memory usage over several hours via Grafana or process metrics","Observe steady linear memory growth correlating with request volume, reaching 3-4GB within 8 hours from a ~500MB baseline"],"environment":"TaskFlow v2.3, Ubuntu 22.04 VM with 8GB RAM, ~200 active users, real-time notifications and webhook integrations enabled","severity":"high","impact":"All ~200 users experience degraded performance by mid-afternoon and complete unusability by end of day. Team is forced to restart the server daily. No data loss reported, but productivity is significantly affected.","recommended_fix":"1. Diff the real-time notification code between v2.2 and v2.3, focusing on request-scoped allocations — look for event listeners, subscriptions, or middleware that attaches per-request objects without cleanup. 2. Check whether notification event emitters or subscription registrations are being added on each request but never removed. 3. Run the application under load with `--inspect` and take heap snapshots at intervals to identify the accumulating object type. 4. As an immediate mitigation, users can set `TASKFLOW_REALTIME_ENABLED=false` to confirm the hypothesis and stop the leak while a fix is developed.","proposed_test_case":"Load test v2.3 with real-time notifications enabled: simulate 200 users at 25 req/hour for 4 hours and assert that memory growth stays below a threshold (e.g., <200MB total growth). Compare the same test with `TASKFLOW_REALTIME_ENABLED=false` to confirm the notification system is the source. Additionally, unit test the notification subscription lifecycle to verify that per-request listeners are properly deregistered.","information_gaps":["Reporter is running a toggle test tomorrow (TASKFLOW_REALTIME_ENABLED=false) — results will confirm or refute the notification hypothesis","WebSocket connection count and notification subscription/listener count from /admin/status not yet collected","Heap dump not yet captured — would pinpoint the exact leaking object type and code path","Unknown whether the leak exists in the notification middleware, the event emitter layer, or a new dependency introduced in v2.3"]}

---
