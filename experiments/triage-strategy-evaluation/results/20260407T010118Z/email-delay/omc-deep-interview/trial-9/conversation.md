# Conversation: email-delay x omc-deep-interview

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the morning vs. afternoon pattern is a really useful clue.

Did anything change about a week ago when this started? For example, a deployment, a change to your email provider or notification settings, or a jump in the number of users or tasks being created? Even something that seemed unrelated at the time could help us narrow this down.

---

### reporter

Hmm, I'm not sure about deployments specifically — I don't have visibility into that. But I do remember someone mentioning that TaskFlow added some kind of daily digest or summary email thing recently. I started getting those marketing-ish digest emails around the same time the delays began, now that I think about it. Not sure if that's related or just a coincidence. Our team size hasn't changed and we're not creating more tasks than usual.

---

### triage-agent

That's a great catch about the digest emails — the timing lines up and it's definitely worth investigating.

Quick question to help us scope this: are other people on your team also seeing the delayed notifications, or is it just you? And is the delay only on task-assignment emails, or are other notification types (like comments or due-date reminders) also arriving late?

---

### reporter

Yeah, it's not just me — other people on my team have mentioned the same thing. Our due date reminders also seem to be delayed, though I notice the assignment notifications more because those are more time-sensitive for us. I think basically all the email notifications are slow, not just one type. The digest emails themselves actually seem to arrive on time ironically, but maybe I'm wrong about that.

---

### triage-agent

That's really helpful — knowing the whole team is affected and it's across notification types makes this clearly a systemic issue, not an account-specific one.

One thing that would help us pinpoint where the delay is happening: next time you get a late notification, could you check the email headers (in Gmail it's the "Show original" option, in Outlook it's "View message source") and look at the timestamp in the `Date:` header? If that timestamp is close to when the task was actually assigned, the delay is in email delivery. If the timestamp is close to when you *received* it (hours later), the delay is on our end — meaning we're slow to send it in the first place. That single data point would tell us exactly where to focus the investigation.

---

### reporter

I haven't done that before but I'll try to check next time. I did notice something though — when I get a late assignment notification, I sometimes check the TaskFlow dashboard and I can see the task was assigned to me hours earlier. So it definitely seems like the delay is on your end sending it, not my email provider being slow to deliver it. I'll try to look at those headers tomorrow morning when the delays are worst and report back.

---

### triage-agent

Thanks for checking — that dashboard observation is really strong evidence the delay is on our side, so the headers are less urgent now (though still useful if you get a chance).

One more thing that would help us zero in: roughly what time do those daily digest emails usually land in your inbox? If they're arriving in the early morning, it would line up perfectly with the theory that the digest job is flooding the email queue right when your team is assigning tasks.

---

### reporter

I think they usually show up around 9am, maybe a little after. That's right when we start our day and begin assigning tasks, so yeah, the timing does overlap. I never really thought about it that way but it makes sense if they're connected somehow.

---

### triage-agent

[RESOLVED] {"title":"Transactional email notifications delayed 2-4 hours, likely due to daily digest job saturating email queue","problem":"All email notifications (task assignments, due-date reminders, etc.) are arriving 2-4 hours late for the past week. The delay is worst in the morning (9-10am) and affects the entire team. This is causing missed deadlines because users don't learn about task assignments promptly.","root_cause_hypothesis":"A recently launched daily digest/summary email feature sends bulk emails around 9am. This likely shares the same email-sending queue or rate-limited provider pipeline as transactional notifications. The digest job floods the queue each morning, pushing time-sensitive notifications to the back. Afternoon notifications are faster because the digest backlog has cleared by then. The digest emails themselves arrive on time because they are enqueued first.","reproduction_steps":["Assign a task to a user at approximately 9:00-9:15am, shortly after the daily digest job runs","Observe the time gap between the assignment timestamp in the database/dashboard and when the notification email is actually sent (check email queue logs)","Compare with a task assigned in the afternoon (e.g., 2pm) and observe the shorter delay","Check the email queue depth and processing times around 9am vs. other times of day","Disable or defer the digest job and verify that morning notification latency returns to normal"],"environment":"Server-side issue affecting all users. Not specific to any email provider, client, or account. Team-wide across all email notification types.","severity":"high","impact":"All users receiving email notifications are affected. Team members are missing task assignment deadlines because they learn about assignments hours late. Morning workflows are most impacted. This undermines the core utility of task-assignment notifications.","recommended_fix":"1. Investigate the email-sending infrastructure — confirm whether digest and transactional emails share a queue or rate limit. 2. Separate transactional notifications into a high-priority queue distinct from bulk digest sends, or implement priority levels within the existing queue. 3. If using an external email provider, check whether the digest volume is hitting API rate limits that delay subsequent sends. 4. Consider scheduling the digest job during off-peak hours (e.g., 7am or overnight) so it doesn't compete with the morning burst of transactional notifications.","proposed_test_case":"Send a batch of digest emails (simulating the daily digest volume), then immediately enqueue a transactional notification. Assert that the transactional notification is sent within 2 minutes regardless of digest queue depth. This validates that priority separation is working correctly.","information_gaps":["Exact date the digest feature was deployed (verifiable from deployment logs)","Email queue architecture — single queue vs. separate queues, provider rate limits","Email header timestamps from a delayed notification (reporter will attempt to provide, but developer can also check send logs directly)","Exact volume of digest emails sent per run and whether it exceeds provider rate limits"]}

---
