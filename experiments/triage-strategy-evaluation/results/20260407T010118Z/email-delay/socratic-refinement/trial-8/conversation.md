# Conversation: email-delay x socratic-refinement

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the morning vs. afternoon pattern is a really useful observation.

To help us dig into this, can you tell me a bit more about the context? Specifically: are other people on your team also experiencing the same delays, or is this something you've primarily noticed with your own notifications? And do you recall if anything changed about a week ago when this started — a new team member joining, a change in how tasks are assigned, or anything else that might have shifted around that time?

---

### reporter

Yeah, it's not just me — other people on my team have mentioned the same thing. Our project manager was complaining about it yesterday actually. As for what changed a week ago... I think that's around when we started getting those new daily digest emails? Like a marketing summary or something. I'm not sure if that's related but the timing lines up. Nothing else changed on our end — same team, same workflow.

---

### triage-agent

That's a really helpful connection — the timing of the daily digest emails lining up with when the delays started is worth exploring.

Can you tell me more about those digest emails? Specifically, around what time do they seem to go out, and roughly how many people on the platform would be receiving them? I'm curious whether the digest send window overlaps with that 9-10am period where you see the worst delays for task notifications.

Also, when the notifications do eventually arrive late — do they all arrive, or have you noticed any that never showed up at all?

---

### reporter

The digest emails seem to arrive around 9am, which yeah, lines up with when the delays are worst. I'm not sure exactly how many people get them — we have maybe 200 users on our TaskFlow instance, so I'd guess everyone gets one? As for notifications going missing, I don't think any have been lost entirely. They all seem to show up eventually, just really late. By the afternoon things seem to mostly catch up.

---

### triage-agent

[RESOLVED] {"title":"Task notification emails delayed 2-4 hours due to daily digest email queue contention","problem":"Task assignment email notifications, which previously arrived within 1-2 minutes, are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and improve by afternoon. The issue began approximately one week ago, coinciding with the introduction of a new daily digest email feature. The problem affects all team members, not just one user.","root_cause_hypothesis":"The daily digest emails (sent to ~200 users around 9am) are being processed through the same email sending queue or worker as transactional task-assignment notifications. The burst of ~200 digest emails saturates the queue each morning, causing task notifications to wait behind the digest batch. As the digest backlog clears through the day, notification latency improves.","reproduction_steps":["Observe the email sending queue around 9am when daily digests are dispatched","Assign a task to a user at ~9:15am, shortly after digest sending begins","Measure time between task assignment and notification email delivery","Compare with the same test at ~3pm when the queue should be idle","Confirm the morning delay is significantly worse than the afternoon delay"],"environment":"TaskFlow instance with approximately 200 users, daily digest email feature recently enabled, shared email sending infrastructure","severity":"high","impact":"All users (~200) on the instance are affected. Delayed task assignment notifications cause team members to miss or be late to deadlines, directly impacting project delivery and team coordination.","recommended_fix":"Investigate whether digest emails and transactional notifications share the same email queue/worker. The fix is likely one of: (1) separate the queues so transactional notifications have priority over bulk digest sends, (2) throttle or stagger the digest send over a longer window to avoid a burst, or (3) use a priority mechanism in the existing queue that sends transactional emails before bulk emails. Option 1 is the most robust long-term solution.","proposed_test_case":"Send a batch of 200 digest emails and simultaneously trigger a task assignment notification. Verify that the task notification is delivered within the expected SLA (e.g., under 2 minutes) regardless of digest queue depth. Repeat the test during the digest send window (9am) to confirm priority handling works under real conditions.","information_gaps":["Exact email infrastructure details (single queue, single worker, third-party service vs. self-hosted SMTP)","Whether the digest feature sends 200 individual emails or uses a batch/bulk API","Whether there are any email rate limits imposed by the sending provider that could compound the issue"]}

---
