# Conversation: email-delay x structured-triage

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you described (worse around 9-10am, better in the afternoon) is really helpful for narrowing this down.

Could you share some environment details? Specifically:
- What email provider are you using (Gmail, Outlook, etc.)?
- Which version of TaskFlow are you on?
- Are the delayed notifications affecting your whole team or just specific users?

This will help us figure out whether the delay is on our sending side or somewhere in the delivery pipeline.

---

### reporter

We use Outlook for email. I believe we're on TaskFlow v2.3.1 but I'd have to double-check with our admin. The delays are affecting my whole team — I've heard from at least 3 or 4 people who've had the same issue. It's definitely not just me. I don't think it's on the email provider side because other emails come through fine, it really seems like it's something on TaskFlow's end.

---

### triage-agent

[RESOLVED] {"title":"Email notifications for task assignments delayed 2-4 hours, worst during morning hours","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now consistently delayed by 2-4 hours. The issue has persisted for approximately one week and affects the entire team (at least 3-4 users confirmed). The delay is most severe for notifications sent around 9-10am, with afternoon notifications being faster but still delayed. This is causing missed deadlines because team members don't learn about task assignments in time.","root_cause_hypothesis":"Most likely a queue backlog or rate-limiting issue in the email sending pipeline. The morning peak pattern (9-10am showing worst delays) suggests the notification queue is overwhelmed during high-activity hours when many tasks are assigned simultaneously, and the queue gradually drains through the afternoon. This could be caused by a recent change in v2.3.x to the email worker (reduced concurrency, new rate limits), a degraded connection to the mail relay, or a growing queue table that slows processing.","reproduction_steps":["Assign a task to a team member in TaskFlow around 9-10am","Note the exact timestamp of the assignment","Monitor when the email notification is received in Outlook","Observe that the notification arrives 2-4 hours after assignment","Repeat in the afternoon and observe a shorter but still present delay"],"environment":"TaskFlow v2.3.1 (approximate), Outlook email provider, multiple users on the same team affected","severity":"high","impact":"Entire teams are missing deadlines because task assignment notifications arrive hours late. At least 3-4 users confirmed affected. Directly impairs the core workflow of assigning and acting on tasks.","recommended_fix":"Investigate the email notification queue/worker: (1) Check queue depth and processing times, especially during 9-10am window. (2) Review email worker logs for errors, retries, or throttling around that timeframe. (3) Check if any recent changes to email sending configuration, worker concurrency, or rate limits were deployed. (4) Verify SMTP relay connection health and response times. (5) Check if the notification jobs table has grown significantly and needs indexing or cleanup.","proposed_test_case":"Send a task assignment notification at peak hours (9-10am) and verify it is delivered within 2 minutes. Run a load test simulating typical morning assignment volume and confirm all notifications are dispatched within the expected SLA.","information_gaps":["Exact TaskFlow version (reporter said 'I believe v2.3.1' but was unsure)","Server-side email queue logs and metrics (not available from reporter)","Whether this coincided with a specific deployment or configuration change","Whether the delay is in TaskFlow's sending or in SMTP relay processing"]}

---
