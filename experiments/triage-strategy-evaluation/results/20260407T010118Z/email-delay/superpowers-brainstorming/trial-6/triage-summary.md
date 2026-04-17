# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours due to queue congestion from daily digest batch send

## Problem
Task assignment notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is worst for notifications sent in the morning (9-10am) and improves in the afternoon. This affects all users and has been occurring for approximately one week. Team members are missing deadlines because they don't learn about task assignments until hours later.

## Root Cause Hypothesis
The new daily digest/marketing summary emails, introduced approximately one week ago, are sent as a morning batch job that floods the shared email sending queue. Transactional emails (task assignment notifications) are enqueued behind the large digest batch, causing them to wait hours for delivery. Morning delays are worst because the queue is fullest after the batch is dispatched; afternoon notifications are faster because the queue has mostly drained by then.

## Reproduction Steps
  1. Assign a task to a user in the morning (9-10am), around the time daily digest emails are being sent
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Repeat the assignment in the afternoon and observe a shorter (but still abnormal) delay
  4. Check the email sending queue depth over the course of a day to confirm the morning spike correlating with digest dispatch

## Environment
Affects all users on the reporter's team. No specific OS/browser dependency — this is a server-side email delivery issue.

## Severity: high

## Impact
All users experience delayed task assignment notifications, causing missed deadlines and reduced team responsiveness. Business-critical workflow disruption.

## Recommended Fix
Separate transactional emails (task notifications) from bulk/batch emails (daily digests) into distinct queues or priority lanes. Transactional emails should be sent with higher priority so they are never blocked behind batch sends. Alternatively, throttle the digest batch send rate to avoid flooding the queue, or schedule digests during off-peak hours. Investigate the email sending infrastructure to confirm a single shared queue is the bottleneck.

## Proposed Test Case
After the fix, send a batch of digest emails and simultaneously trigger a task assignment notification. Verify the task notification arrives within 2 minutes regardless of digest queue depth. Load test with a digest batch size matching production volume to confirm notifications remain timely under realistic conditions.

## Information Gaps
- Exact email queue implementation and infrastructure (single queue vs. multiple, third-party provider vs. self-hosted)
- Size of the daily digest batch (number of recipients)
- Precise time the digest batch job runs each morning
- Whether any email rate limits or provider throttling are also contributing
