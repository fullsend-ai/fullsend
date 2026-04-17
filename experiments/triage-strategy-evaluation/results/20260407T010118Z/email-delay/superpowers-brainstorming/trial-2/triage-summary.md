# Triage Summary

**Title:** Email notification delivery delayed 2-4 hours (email queue suspected)

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now arriving 2-4 hours late. The delay is worst for emails sent in the morning (9-10am) and gradually improves through the afternoon. In-app notifications are unaffected — they appear promptly. The issue affects all users across all email providers (Gmail and Outlook confirmed) and began approximately one week ago.

## Root Cause Hypothesis
The email sending queue or worker is bottlenecked. Since in-app notifications generate on time, notification creation is working correctly, but the email dispatch pipeline is falling behind. The morning-heavy pattern suggests either: (a) a batch job or queue that accumulates overnight and drains slowly during the day, (b) a reduced number of email worker processes or threads, or (c) a rate limit or throttle on the mail relay that was introduced or tightened ~1 week ago.

## Reproduction Steps
  1. Assign a task to any user in the morning (9-10am)
  2. Observe that the in-app notification appears within seconds
  3. Monitor the assigned user's email inbox — the email notification arrives 2-4 hours later
  4. Repeat in the afternoon and observe a shorter (but still abnormal) delay

## Environment
Affects all users on the reporter's team. Mix of Gmail and Outlook recipients. Issue began approximately one week ago. No reporter-side changes mentioned.

## Severity: high

## Impact
Team members are missing task assignment deadlines because they rely on email notifications and don't see assignments for hours. Affects the entire team's workflow and deadline adherence.

## Recommended Fix
1. Check email queue depth and worker health — look for a growing backlog, especially overnight and in early morning. 2. Review changes deployed ~1 week ago that touched the email pipeline, queue configuration, worker scaling, or mail relay settings. 3. Check mail relay/SMTP provider for new rate limits, throttling, or delivery issues. 4. Examine email worker logs for errors, retries, or connection timeouts. 5. If a queue backlog is confirmed, consider scaling workers or splitting email dispatch into a dedicated high-priority queue for time-sensitive notifications like task assignments.

## Proposed Test Case
Send a task assignment notification and assert that the corresponding email is dispatched (enqueued to the mail relay) within 60 seconds of the in-app notification being created. This validates that the email pipeline keeps pace with notification generation.

## Information Gaps
- Exact infrastructure change that occurred ~1 week ago (deployment, config, provider change) — requires internal investigation, not reporter knowledge
- Current email queue depth and worker metrics — needs monitoring/observability data
- Whether other email notification types (e.g., comment mentions, due date reminders) are also delayed, or only task assignments
