# Conversation: email-delay x superpowers-brainstorming

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the morning vs. afternoon pattern is a really useful clue.

To help narrow this down: is this delay affecting all users on your team, or is it limited to certain people? Specifically:

1. **Everyone** — all users see the same delays
2. **Certain users** — some people get timely notifications while others don't
3. **Certain email domains** — e.g., people with @company.com are delayed but @gmail.com are fine
4. **Not sure** — you've only observed your own notifications

This will help us figure out whether it's a system-wide queuing issue or something more targeted.

---

### reporter

It's affecting everyone on my team as far as I can tell. I've heard complaints from at least 3 or 4 other people about the same thing — late assignment notifications, especially in the morning. We're all on the same company email domain though, so I can't really say whether it's domain-specific or not. I don't think anyone on the team uses a non-company email.

---

### triage-agent

That's helpful — a team-wide issue with a clear morning pattern points toward a system-level queuing problem rather than anything user-specific.

Do you know if anything changed about a week ago when this started? For example:

1. **A product update or deployment** — a new version of TaskFlow was rolled out
2. **An infrastructure or email provider change** — migration to a new email service, DNS changes, etc.
3. **A volume change** — your team grew, or a new workflow started generating significantly more notifications
4. **Nothing obvious** — it just started happening with no visible trigger

This will help us figure out whether we're looking at a regression, a capacity issue, or something else entirely.

---

### reporter

I'm not 100% sure, but I think there was some kind of product update recently. I noticed we started getting these daily digest emails — like a summary of project activity or something marketing-related. They started showing up around the same time the delays began, maybe a week ago? I'm not sure if that's related or just a coincidence. I don't think our team size changed or anything like that.

---

### triage-agent

[RESOLVED] {"title":"Task assignment email notifications delayed 2-4 hours, correlating with new daily digest feature","problem":"Transactional email notifications for task assignments, which previously arrived within 1-2 minutes, are now delayed by 2-4 hours. The issue is team-wide, started approximately one week ago, and is worst in the morning (9-10am). The onset coincides with the introduction of a new daily digest/marketing summary email feature.","root_cause_hypothesis":"The new daily digest emails (introduced ~1 week ago) are likely sharing a queue or worker pool with transactional task-assignment notifications. Digest emails are probably batched and sent in the morning, saturating the email sending capacity and causing transactional notifications to back up behind them. Afternoon notifications are less delayed because the digest backlog has cleared by then.","reproduction_steps":["Assign a task to a user in the morning (9-10am)","Note the timestamp of the assignment","Check when the email notification is received","Compare with the same test in the afternoon (2-3pm)","Observe that morning notifications are delayed 2-4 hours while afternoon delays are shorter"],"environment":"Affects all users on the team; all on the same company email domain. Standard TaskFlow deployment with recently added daily digest email feature.","severity":"high","impact":"Team-wide. Users are missing task assignment deadlines because they don't learn about assignments for hours. Directly impacts team productivity and deadline compliance.","recommended_fix":"1. Check whether daily digest emails and transactional notifications share the same email queue/worker pool. 2. If so, separate them — use a dedicated high-priority queue for transactional notifications and a lower-priority queue for bulk digests. 3. Review the scheduling of digest emails — if they're all enqueued at once in the morning, implement rate limiting or stagger their delivery. 4. Consider checking email provider rate limits to ensure the digest volume isn't triggering throttling.","proposed_test_case":"Send a task assignment notification while a batch of digest emails is being processed. Verify the assignment notification is delivered within 2 minutes regardless of digest queue depth. Also verify under load: enqueue 1000 digest emails, then send a transactional notification, and confirm the transactional notification is not blocked behind the digest batch.","information_gaps":["Exact email sending infrastructure (self-hosted SMTP, SendGrid, SES, etc.)","Whether digest emails and transactional emails actually share a queue (needs code/infra verification)","Exact time the daily digest job runs","Email provider rate limits and whether they're being hit"]}

---
