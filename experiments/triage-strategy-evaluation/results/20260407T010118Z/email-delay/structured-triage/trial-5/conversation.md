# Conversation: email-delay x structured-triage

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you described (worse in the morning, faster in the afternoon) is really helpful for narrowing this down.

Could you share some environment details? Specifically:
- What version of TaskFlow are you running?
- Is this a self-hosted instance or our cloud offering?
- What email provider are you using (e.g., Gmail, Outlook, corporate Exchange server)?
- Has anything changed in your infrastructure or configuration around the time this started?

---

### reporter

We're on TaskFlow v2.3.1, it's self-hosted. Email goes through our SMTP relay — I'm not sure of the exact setup, our IT team manages that part. As for changes, I think there was some product update about a week ago? I remember seeing something about a new daily digest feature rolling out. I'm not sure if that's related but the timing lines up roughly. We use Outlook for email on our end but I don't think it's an Outlook issue since everyone on the team is affected regardless of their mail client.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours since ~v2.3.1 update (likely related to daily digest feature)","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is worse for notifications sent in the morning (9-10am) and somewhat shorter in the afternoon. All team members are affected regardless of mail client.","root_cause_hypothesis":"The daily digest feature introduced around a week ago likely changed email notification queuing or batching behavior. Morning notifications may be getting caught in a digest aggregation window or a queue that builds up overnight and drains slowly. The timing correlation between the feature rollout and the onset of delays strongly suggests the digest feature is either batching notifications that should be sent immediately, or its background processing is competing with real-time notification delivery.","reproduction_steps":["Use a self-hosted TaskFlow v2.3.1 instance with SMTP relay configured","Assign a task to a user around 9-10am","Observe that the email notification arrives 2-4 hours later instead of within 1-2 minutes","Repeat in the afternoon and observe a shorter but still present delay"],"environment":"TaskFlow v2.3.1, self-hosted instance, email delivered via corporate SMTP relay, recipients using Outlook (but issue is client-independent)","severity":"high","impact":"Entire team is affected. Delayed notifications cause missed deadlines because assignees don't learn about tasks for hours. Business-critical workflow disruption.","recommended_fix":"Investigate the daily digest feature's interaction with real-time notifications. Check whether: (1) the notification queue or scheduler was modified to batch emails for digest purposes, (2) real-time notifications are being routed through the digest pipeline instead of the immediate-send path, (3) a background job for digest processing is blocking or starving the real-time notification worker. Review the email sending code changes introduced with the digest feature and verify that immediate notifications bypass digest batching entirely.","proposed_test_case":"With the daily digest feature enabled, assign a task and verify that the assignment notification email is delivered within 2 minutes. Repeat at multiple times of day (morning and afternoon) to confirm no time-of-day regression. Also verify that digest emails are generated separately and do not interfere with real-time notification delivery.","information_gaps":["Server-side email queue logs showing where notifications are dwelling","Exact SMTP relay configuration and whether any relay-side throttling exists","Whether the daily digest feature is explicitly enabled for this instance or was auto-enabled by the update","Whether disabling the digest feature resolves the delay (quick validation of hypothesis)"]}

---
