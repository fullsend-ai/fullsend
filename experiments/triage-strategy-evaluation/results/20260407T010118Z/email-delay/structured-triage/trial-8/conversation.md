# Conversation: email-delay x structured-triage

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you described (worse in the morning, faster in the afternoon) is really helpful.

Could you share some environment details? Specifically:
- Which version of TaskFlow are you running (or is this the cloud-hosted version)?
- What email provider are the delayed notifications going to (e.g., Gmail, Outlook, corporate Exchange)?
- Has anything changed in your environment in the past week — such as an app update, a mail server migration, or a new email security policy?

---

### reporter

We're on TaskFlow v2.3.1, self-hosted. Email goes through our corporate SMTP relay — most of us use Outlook. As for changes, I think there was a product update about a week ago? I started getting these new daily digest emails from TaskFlow around the same time the delays started, but I'm not sure if that's related or just a coincidence. No changes on our mail server side as far as I know.

---

### triage-agent

Thanks for those details — very helpful. You mentioned that new daily digest emails from TaskFlow started around the same time the delays began. That's a really interesting lead.

Could you tell me a bit more about those digests? Specifically:
- Roughly what time of day do the daily digest emails arrive?
- When a delayed notification finally comes through, does it arrive around the same time as the digest, or at a completely different time?

I'm trying to determine whether the notification delays might be tied to the digest feature inadvertently batching or queuing individual notifications.

---

### reporter

The digest emails usually show up in my inbox around 9am, give or take. As for the delayed notifications — they don't arrive at the same time as the digest exactly. It's more like they trickle in over the next few hours. Like if I get assigned a task at 9:15am, I might not get the notification until 11am or later. But if I get assigned something at 2pm, it usually comes through within maybe 20-30 minutes. So it feels like whatever's happening in the morning clears up by the afternoon. I hadn't really connected the dots between the digest and the delays until you asked, but now that you mention it, the timing does line up pretty suspiciously.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours after v2.3.1 update, likely caused by daily digest feature saturating or blocking the mail queue","problem":"Since approximately one week ago (coinciding with a product update to v2.3.1), task assignment email notifications are delayed by 2-4 hours when sent in the morning, and 20-30 minutes in the afternoon. Previously notifications arrived within 1-2 minutes. This is causing users to miss task deadlines.","root_cause_hypothesis":"The daily digest feature introduced in the recent update is likely saturating or blocking the email sending queue when it runs around 9am. The digest job may be processing a large batch of emails synchronously on the same queue as individual notifications, starving real-time notifications of throughput. As the digest backlog clears through the morning, individual notification latency gradually improves, explaining why afternoon notifications are only mildly delayed.","reproduction_steps":["Set up a self-hosted TaskFlow v2.3.1 instance with daily digest emails enabled","Configure email delivery through an SMTP relay","Assign a task to a user at approximately 9:15am (shortly after the digest is scheduled)","Observe that the assignment notification email is delayed by 2+ hours","Assign another task to the same user at approximately 2:00pm","Observe that this notification arrives within 20-30 minutes"],"environment":"TaskFlow v2.3.1, self-hosted, corporate SMTP relay, Outlook email clients","severity":"high","impact":"All users receiving email notifications are affected. Morning notifications are severely delayed (2-4 hours), causing users to miss task assignment deadlines. This affects team coordination and task handoff workflows.","recommended_fix":"Investigate the email sending architecture in v2.3.1, specifically: (1) Check whether the daily digest job and individual notifications share the same mail queue or worker pool. (2) Inspect the mail queue depth and processing rate around 9am when the digest runs. (3) If they share a queue, separate real-time notifications onto a higher-priority queue or dedicated worker. (4) Consider making the digest job rate-limited or sending digests in smaller batches to avoid starving the notification pipeline.","proposed_test_case":"With daily digests enabled, trigger a digest send for N users and simultaneously enqueue an individual task assignment notification. Assert that the individual notification is delivered within the expected SLA (e.g., under 2 minutes) regardless of digest queue depth. Additionally, add a queue-depth monitoring test that alerts when the mail queue exceeds a threshold.","information_gaps":["Server-side mail queue logs or TaskFlow application logs from the morning period would confirm the queue saturation hypothesis","Whether other self-hosted v2.3.1 users with daily digests enabled experience the same delay pattern","The exact number of users/tasks generating digest emails (queue volume)"]}

---
