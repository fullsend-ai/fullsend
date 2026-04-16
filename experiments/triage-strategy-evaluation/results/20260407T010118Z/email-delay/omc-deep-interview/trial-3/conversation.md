# Conversation: email-delay x omc-deep-interview

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you noticed (worse in the morning, better in the afternoon) is a really useful clue.

To help us narrow down the cause: did anything change about a week ago when this started? For example, were there any updates to your organization's setup, a change in the number of users or teams, or did you notice any other parts of TaskFlow behaving differently around the same time?

---

### reporter

Hmm, I'm not 100% sure what changed on the backend, but I do remember that around a week ago we started getting these new daily digest emails — like a marketing summary or something. They seem to arrive in the morning too. I don't know if that's related but the timing lines up. Nothing else changed on our end that I'm aware of — same number of users, same setup.

---

### triage-agent

That's a really helpful connection — the timing of those daily digest emails lining up with when the delays started is worth investigating.

To help us pin down the pattern more precisely: are *all* task notifications delayed (assignments, due date reminders, comments, etc.), or is it specifically task assignment notifications? And is this affecting everyone on your team, or have some people noticed their notifications arriving on time?

---

### reporter

I've mainly noticed it with task assignments since that's what I rely on most, but now that you mention it, I think due date reminders have also been late. I haven't paid as much attention to comment notifications. As for the team — yeah, it seems to be affecting everyone. A couple of my team members have complained about late assignment emails too. Nobody's said theirs are arriving on time.

---

### triage-agent

That's really useful context — it does sound like this is affecting your whole team across multiple notification types.

One thing that would help us figure out whether the delay is happening on TaskFlow's end or somewhere in email delivery: could you (or a teammate) check the headers on one of the delayed notification emails? Most email clients let you view the full message headers — we'd be looking at the `Date` header (when TaskFlow says it sent the email) versus when it actually landed in your inbox. If those are close together, the delay is on our side before sending; if the `Date` header shows the email was composed hours before delivery, something in the email pipeline is holding it up.

Alternatively, if checking headers isn't practical — do you happen to know if your organization uses any email filtering or security gateway (like Mimecast, Proofpoint, or a Google Workspace routing rule) that incoming emails pass through?

---

### reporter

I don't think we use any special email filtering — it's just regular email, no Mimecast or anything like that as far as I know. I tried looking at the headers on one of the delayed emails like you suggested. I'm not super familiar with reading them, but the Date header seemed to show a time that was pretty close to when the email actually arrived in my inbox — like maybe a few minutes before. So it seems like the email just wasn't sent until hours after the task was actually assigned. That would point to something on TaskFlow's end holding it up before it even gets sent out, right?

---

### triage-agent

[RESOLVED] {"title":"Transactional email notifications delayed 2-4 hours, likely caused by new daily digest emails saturating the send queue","problem":"Since approximately one week ago, task assignment and due date reminder emails are arriving 2-4 hours late. The delay is worst in the morning (9-10am) and less severe in the afternoon. Email headers confirm the delay occurs before sending (TaskFlow holds the email, not an external mail pipeline). The issue is team-wide, not user-specific.","root_cause_hypothesis":"A new daily digest/marketing email feature was rolled out approximately one week ago. These digest emails are likely sent in bulk during the morning, saturating a shared email send queue or exhausting a rate limit with the email provider. Transactional notifications (task assignments, due date reminders) sit behind the digest batch in the queue, causing multi-hour delays. The afternoon improvement aligns with the digest batch having cleared by then.","reproduction_steps":["Assign a task to a user at approximately 9:00 AM (when daily digests are being sent)","Note the timestamp of the assignment in the application","Observe when the notification email is actually sent (check email send logs or queue)","Compare with the same action performed in the afternoon to confirm the timing differential","Check the email queue depth and processing rate during morning digest sends"],"environment":"Affects the reporter's entire team. Standard email setup with no third-party filtering (no Mimecast, Proofpoint, etc.). Issue began approximately one week ago, coinciding with the introduction of daily digest emails.","severity":"high","impact":"Team-wide. Users are missing task assignment deadlines because they don't learn about assignments until hours after they're made. Multiple notification types affected (assignments confirmed, due date reminders likely, comment notifications unknown).","recommended_fix":"1. Investigate whether transactional notifications and digest/marketing emails share the same send queue or email provider rate limit. 2. Separate transactional emails (assignments, reminders) into a high-priority queue distinct from bulk digest sends. 3. If using a single email provider API key, check if the digest volume is hitting rate limits that block transactional sends. 4. Consider sending digests during off-peak hours or throttling digest throughput to leave headroom for transactional email.","proposed_test_case":"Send a batch of digest emails (simulating the daily digest volume) and simultaneously trigger a task assignment notification. Verify that the assignment notification is sent within 2 minutes regardless of digest queue depth. This confirms that queue separation or prioritization is working correctly.","information_gaps":["Exact volume of daily digest emails being sent (how many recipients per batch)","Whether comment notifications are also delayed (reporter was unsure)","Which email sending infrastructure TaskFlow uses (in-house SMTP, SendGrid, SES, etc.)","Whether the email queue is shared or already segmented by email type"]}

---
