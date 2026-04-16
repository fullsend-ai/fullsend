# Conversation: email-delay x structured-triage

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the time-of-day pattern you described is really helpful for narrowing this down.

Could you share some environment details? Specifically:
- What version of TaskFlow are you running?
- What email provider are the notifications being delivered to (e.g., Gmail, Outlook/Exchange, corporate mail server)?
- Is this a self-hosted or cloud-hosted TaskFlow instance?

This will help us figure out whether the delay is on the sending side or the receiving side.

---

### reporter

We're on TaskFlow v2.3.1. Our company uses Outlook/Exchange for email. I'm not 100% sure if it's self-hosted or cloud — I think it's cloud-hosted but our IT team would know better. I will say the delays definitely seem to be on the sending side because when the emails do arrive, the timestamp in the email header shows they were sent late, not just delivered late. At least that's what it looked like when I checked a couple of them.

---

### triage-agent

Thanks for checking those email headers — that's a strong clue that the delay is on the sending side rather than mail delivery.

To help developers reproduce this reliably, could you walk us through the exact steps? For example:
1. Who assigns the task (role/permissions)?
2. How is the task assigned (drag-and-drop on a board, editing the task detail page, bulk assignment, API, etc.)?
3. After assigning, roughly how long until the notification email arrives?

Also — does this happen for *every* task assignment notification, or only certain types (e.g., only new assignments, not re-assignments or mentions)?

---

### reporter

Usually it's me or another manager assigning tasks — we have admin/manager roles. We mostly assign through the task detail page, just pick someone from the assignee dropdown and save. Nothing fancy. The notification typically takes 2-4 hours in the morning, maybe 30-60 minutes in the afternoon. And yeah, it happens for every type of email notification as far as I can tell — assignments, due date reminders, all of it. It's not specific to one type. Before about a week ago everything was basically instant, like under a minute.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours (regression in sending pipeline, ~1 week)","problem":"All email notifications (task assignments, due date reminders, etc.) are being sent with significant delays. Morning notifications (9-10am) are delayed 2-4 hours; afternoon notifications are delayed 30-60 minutes. This started approximately one week ago; prior to that, notifications arrived within a minute.","root_cause_hypothesis":"The time-of-day pattern (worse in morning, improving through afternoon) strongly suggests a queued/batched email sending pipeline that is either backed up, throttled, or whose background worker is under-provisioned. A likely cause is a change ~1 week ago to the email job queue configuration, a degraded background worker, or a new rate limit being hit — the morning backlog accumulates overnight or during peak assignment hours and gradually drains through the day.","reproduction_steps":["Log in as a user with admin/manager role on TaskFlow v2.3.1","Open any task's detail page","Select a user from the assignee dropdown and save","Observe the time the assignment is made vs. when the notification email is actually sent (check email headers for the sending timestamp)","Repeat in the morning (9-10am) for maximum delay reproduction"],"environment":"TaskFlow v2.3.1, cloud-hosted instance (to be confirmed with IT), recipients on Outlook/Exchange corporate mail","severity":"high","impact":"All users receiving email notifications are affected. Team members are missing task assignment deadlines because they learn of assignments hours late. This disrupts workflow for managers and assignees organization-wide.","recommended_fix":"Investigate the email sending pipeline on the server side: (1) Check the email job queue (e.g., Sidekiq, Celery, or similar) for backlog depth and processing rate, especially during morning hours. (2) Review any changes deployed ~1 week ago that may have altered queue concurrency, rate limits, or worker count. (3) Check for resource contention on the worker process (CPU, memory, database connections). (4) Verify SMTP send rate limits with the outbound mail provider — a new throttle could explain the draining-backlog pattern.","proposed_test_case":"Trigger a task assignment notification and assert that the email is enqueued and sent within 60 seconds. Include a time-based integration test that assigns tasks during simulated peak load and verifies send latency stays under the SLA threshold.","information_gaps":["Exact hosting details (cloud vs. self-hosted) — reporter was unsure, IT team would confirm","Server-side email queue logs and metrics (not available to the reporter)","Whether any deployment or configuration change coincided with the onset ~1 week ago"]}

---
