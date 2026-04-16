# Conversation: email-delay x structured-triage

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you noticed (worse in the morning around 9-10am) is a really helpful clue.

Could you walk me through the exact steps to reproduce this? For example:
1. How are you assigning the task — through the web UI, API, or another method?
2. When the notification finally arrives, does it come via email only, or are in-app notifications also delayed?
3. Are all users experiencing this delay, or is it specific to certain teams or accounts?

This will help us narrow down where in the pipeline the delay is occurring.

---

### reporter

I assign tasks through the web UI, just the normal way — click on a task, assign it to someone. The in-app notifications I think show up fine, it's really just the email notifications that are delayed. I haven't checked the in-app timing super carefully though, I mainly rely on email. As for whether it's everyone — yeah, I've heard from a few other team leads that they're seeing the same thing. It doesn't seem to be specific to certain people. One thing I noticed is that this started about a week ago, around the same time I started getting those new daily digest emails in the morning. Not sure if that's related but the timing lines up.

---

### triage-agent

Thanks for that context — the correlation with the new daily digest emails is a great observation and could definitely be relevant.

Could you share some environment details? Specifically:
- What version of TaskFlow are you running (you can find this under Settings → About)?
- Is this a self-hosted instance or TaskFlow Cloud?
- What email provider are your team's accounts using (e.g., Gmail, Outlook/Exchange, something else)?

This will help us figure out whether the delay is on the sending side (TaskFlow's email queue) or the receiving side (email provider throttling).

---

### reporter

I think we're on v2.3.1 — I checked that a couple days ago when this started bugging me. It's self-hosted, our IT team manages it. As for email provider, we use Outlook/Exchange for our company email. But I'd be surprised if it's on the receiving side since other emails come through fine — it's only the TaskFlow notifications that are slow.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours since daily digest feature rollout (v2.3.1, self-hosted)","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is worst for notifications sent in the morning (9-10am) and less severe in the afternoon. In-app notifications appear unaffected. Multiple team leads across the organization report the same issue.","root_cause_hypothesis":"The new daily digest email feature (introduced ~1 week ago, coinciding with the start of delays) is likely flooding the email sending queue each morning. The digest batch job probably runs at a scheduled morning time, enqueuing a large volume of digest emails that block or throttle individual notification emails behind them. This explains why morning delays are worst (digest batch is fresh) and afternoon delays are shorter (digest backlog has cleared).","reproduction_steps":["Log into TaskFlow web UI (self-hosted, v2.3.1)","Open any task and assign it to a user","Note the assignment timestamp","Check the assignee's Outlook/Exchange inbox for the notification email","Observe that the email arrives 2-4 hours later (worst if assigned around 9-10am)"],"environment":"TaskFlow v2.3.1, self-hosted instance, Outlook/Exchange email provider","severity":"high","impact":"Organization-wide — multiple teams are missing task deadlines because assignment notifications arrive hours late. Only email notifications are affected; in-app notifications appear to work normally.","recommended_fix":"Investigate the email sending queue and its interaction with the daily digest feature. Specifically: (1) Check if the daily digest batch job shares the same email queue as transactional notifications; (2) Check SMTP rate limits and queue concurrency settings; (3) Consider separating transactional notification emails into a priority queue distinct from bulk digest emails, or throttling digest sends to avoid starving real-time notifications.","proposed_test_case":"Assign a task via the web UI at 9:30am (peak delay window) and verify the email notification arrives within 2 minutes. Run this test both with and without the daily digest feature enabled to confirm the digest is the cause of the delay.","information_gaps":["Server-side email queue logs showing queue depth and send times (developer must check directly)","Exact schedule and configuration of the daily digest batch job","Whether in-app notifications are truly unaffected or also slightly delayed (reporter was uncertain)"]}

---
