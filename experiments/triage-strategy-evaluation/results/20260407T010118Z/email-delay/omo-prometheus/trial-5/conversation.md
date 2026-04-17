# Conversation: email-delay x omo-prometheus

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the time-of-day pattern is a useful clue.

To help me scope this: is this delay affecting **all users** on your team, or just your account specifically? And are other notification channels (e.g., in-app notifications, Slack/webhook integrations if you use them) also delayed, or is it only email?

Also, do you know if anything changed about a week ago when this started — a new deployment, a change in team size, or a spike in task volume?

---

### reporter

It's affecting everyone on my team, not just me. I checked with a few colleagues and they're all seeing the same delays on email notifications. We don't use Slack integration, but I think the in-app notifications might be fine — I haven't paid super close attention to that though. I mainly rely on email.

As for what changed... I'm not sure about deployments, but I did notice we started getting these new daily digest emails about a week ago? Like a marketing summary thing that shows up in the morning. I don't know if that's related but the timing lines up. Our team size hasn't changed.

---

### triage-agent

That daily digest email is a really helpful clue — the timing lines up perfectly.

Two quick things that would help me narrow this down:

1. **What time do those daily digest emails typically land in your inbox?** If they arrive around 9am and that's also when you see the worst delays on task notifications, that's a strong signal they're related.
2. **Could you do a quick test on the in-app notifications?** Next time someone assigns you a task, check whether the in-app notification appears right away even though the email is delayed. That would tell us whether the problem is in the email pipeline specifically or something deeper in the notification system.

This will help us figure out whether the digest job is likely saturating the email queue.

---

### reporter

Yeah the digest emails usually show up around 9am, maybe a little after. That's right when the worst delays happen for task notifications.

I'll try to pay attention to the in-app notifications next time, but I'm pretty sure those are working normally — I think I've seen the little badge appear in the app before the email comes through. I just don't usually have TaskFlow open all day so I rely on email to know when something's been assigned to me.

---

### triage-agent

Thanks — this is lining up clearly. Here's my working theory:

The new daily digest feature that launched about a week ago appears to be sending a batch of emails around 9am. That batch is likely flooding the email queue, which means your transactional emails (like task assignment notifications) get stuck behind it and take hours to drain through. That explains why mornings are worst (digest is running) and afternoons are better (the queue has caught up).

The fact that in-app notifications seem fine supports this — the problem isn't in how notifications are *generated*, it's in how emails are *sent*.

One thing that would help the engineering team size up the fix: **roughly how many people are in your organization on TaskFlow?** If the digest is going out to hundreds or thousands of users through the same email pipeline as your task notifications, that's the bottleneck. Even a ballpark would help.

---

### reporter

We have about 200 people on TaskFlow. Yeah your theory makes sense to me — if all those digest emails are going through the same pipe as our task notifications, I can see how that would clog things up. Is there a way to fix this without getting rid of the digest entirely? I don't mind getting the digest, I just need my task assignments to come through quickly.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours due to daily digest batch job saturating email queue","problem":"Transactional email notifications (task assignments, etc.) are arriving 2-4 hours late for all users. The delay is worst in the morning (9-10am) and improves in the afternoon. This started approximately one week ago, coinciding with the launch of a new daily digest email feature. In-app notifications are unaffected.","root_cause_hypothesis":"The new daily digest feature sends a batch of marketing/summary emails to all ~200 users around 9am through the same email pipeline used for transactional notifications. This flood of digest emails saturates the queue, causing transactional emails (task assignments) to wait behind them. As the digest backlog drains through the day, afternoon transactional emails experience shorter delays.","reproduction_steps":["Assign a task to a user around 9-10am (when daily digest emails are being sent)","Observe that the in-app notification appears promptly","Observe that the email notification arrives 2-4 hours later","Repeat the same assignment in the afternoon and observe a shorter (but still present) delay"],"environment":"Organization with ~200 users on TaskFlow. No Slack integration in use. Users primarily rely on email notifications. Daily digest feature launched approximately one week ago.","severity":"high","impact":"All users in the organization are affected. Users who rely on email notifications (rather than keeping the app open) are missing task assignment deadlines because they don't learn about assignments for hours. This directly impacts team productivity and deadline adherence.","recommended_fix":"Separate transactional emails (task assignments, mentions, etc.) from bulk/marketing emails (daily digests) using either: (1) a priority queue where transactional emails are processed first, (2) separate email sending pipelines/workers for transactional vs. bulk, or (3) rate-limiting the digest batch job so it doesn't monopolize the email sending capacity. Investigate the email service configuration (SendGrid, SES, etc.) for queue depth, throughput limits, and whether a priority tier is available. As an immediate mitigation, consider scheduling the digest job during off-peak hours (e.g., 6am or overnight).","proposed_test_case":"After implementing the fix, send a batch of 200 digest emails and simultaneously trigger a task assignment notification. Verify the task assignment email arrives within 2 minutes regardless of digest batch status. Run this test during the digest sending window to confirm transactional emails are no longer blocked.","information_gaps":["Which email service/provider is used (SendGrid, SES, self-hosted SMTP, etc.) and its throughput limits","Whether the email pipeline has any existing queue prioritization mechanism","Exact queue depth and processing rate metrics from the email service during the 9am window","Whether the digest is sent as 200 individual emails or uses a bulk sending API"]}

---
