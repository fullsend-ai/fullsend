# Conversation: email-delay x superpowers-brainstorming

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the morning pattern is a really useful clue. To help narrow this down: is this delay affecting all users on your team, or just some?

1. **All users** — everyone is seeing delays
2. **Most users** — a few seem unaffected
3. **Just me / a few people** — most of the team gets notifications on time
4. **Not sure** — you've only heard from a few people

Also, do you know if anything changed about a week ago — a new deployment, migration, or configuration change — around when this started?

---

### reporter

It's affecting all users as far as I can tell — I've heard complaints from at least 4-5 people on my team, and another team lead mentioned the same thing. Nobody seems to be getting timely notifications in the morning.

As for what changed, I'm not 100% sure but I think there was some new email feature rolled out around that time? We started getting these daily digest or summary emails — I think they come in the morning. Not sure if that's related but the timing lines up roughly.

---

### triage-agent

That's really helpful — the daily digest feature launching around the same time is a strong lead. One more question to help us zero in:

Do you know roughly what time the daily digest emails arrive? Specifically, I'm wondering if the delay window (9–10am) lines up with when digests are being sent out.

1. **Yes, digests arrive around 9–10am** — right when the delays are worst
2. **Digests arrive at a different time** — e.g., early morning, midday, etc.
3. **Not sure exactly when they arrive** — I'd have to check

This will help us determine whether the digest sending job is likely saturating the email queue and blocking real-time notifications.

---

### reporter

Yeah, I think the digest emails do come in around 9am — that's when I usually see them in my inbox. So yeah, option 1, it lines up with when the delays are worst. I hadn't really connected the two until you mentioned it, but now that you say it, the timing does match up pretty well.

---

### triage-agent

[RESOLVED] {"title":"Daily digest email job blocks real-time notification delivery, causing 2-4 hour delays","problem":"Since the daily digest feature was launched approximately one week ago, task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and affect all users across multiple teams. Afternoon notifications are also delayed but less severely.","root_cause_hypothesis":"The daily digest email job, which runs around 9am, is saturating or blocking the shared email sending queue. Real-time task assignment notifications are queued behind a large batch of digest emails, causing them to wait until the digest batch completes. The afternoon improvement suggests the queue gradually drains, but never fully catches up during peak hours.","reproduction_steps":["Assign a task to a user in the morning around 9-10am when daily digests are being sent","Observe that the notification email arrives 2-4 hours late instead of within 1-2 minutes","Repeat the same assignment in the late afternoon and observe a shorter (but still present) delay"],"environment":"Production environment, all users affected, started approximately one week ago coinciding with daily digest feature rollout","severity":"high","impact":"All users across multiple teams are missing task assignment deadlines because notifications arrive hours late. This directly affects team productivity and deadline adherence.","recommended_fix":"Investigate whether real-time notifications and digest emails share the same email sending queue or worker pool. Likely fixes in order of preference: (1) Use a separate high-priority queue/worker for real-time notifications so they bypass digest batch processing, (2) Rate-limit or throttle the digest job so it doesn't monopolize the email pipeline, (3) Move digest sending to an off-peak window (e.g., 6am or overnight) as a quick mitigation.","proposed_test_case":"Send a batch of digest emails (simulating the morning digest job) and simultaneously trigger a real-time task assignment notification. Verify the real-time notification is delivered within the expected SLA (< 2 minutes) regardless of digest queue depth.","information_gaps":["Exact email queue architecture (shared queue vs. separate queues) — answerable from code inspection","Volume of digest emails sent per morning run — available from production metrics","Whether the email provider has rate limits that the digest job is hitting — check provider docs and logs"]}

---
