# Triage Summary

**Title:** All email notifications delayed 2-4 hours, correlating with daily digest feature launch

## Problem
Since approximately one week ago, all TaskFlow email notifications (task assignments, due date reminders, and likely others) are arriving 2-4 hours late. Delays are worst in the morning (9-10am) and improve in the afternoon. This affects all team members and is causing missed deadlines.

## Root Cause Hypothesis
The newly introduced daily digest email feature, which sends around 9am, is likely flooding a shared email sending queue/pipeline. The digest likely generates a large batch of emails for all users simultaneously, saturating the queue and causing transactional notifications (assignments, reminders) to back up behind it. The morning peak correlates perfectly with digest send time, and afternoon improvement is consistent with the queue gradually draining.

## Reproduction Steps
  1. Assign a task to a user around 9:00-9:15am (shortly after the daily digest sends)
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Repeat the same assignment in the afternoon (e.g., 3pm)
  4. Observe that the notification still arrives late but with less delay
  5. Compare with email delivery times from before the digest feature was enabled

## Environment
Affects all team members (confirmed with 4-5 people). Production environment. Issue began approximately one week ago, coinciding with the launch of a new daily digest email feature.

## Severity: high

## Impact
All team members are missing task assignments and deadline reminders. Users are not learning about assigned tasks for hours, directly causing missed deadlines. No workaround has been identified short of manually checking TaskFlow for new assignments.

## Recommended Fix
1. Investigate the email sending infrastructure — confirm whether the daily digest and transactional notifications share a single email queue/worker pool. 2. If shared, implement priority queuing: transactional emails (assignments, reminders) should have higher priority than bulk emails (digests). 3. Consider rate-limiting or throttling digest sends, or moving them to a separate queue/worker. 4. As an immediate mitigation, consider shifting the digest send time to off-peak hours (e.g., 6am or overnight) to avoid competing with morning transactional email volume.

## Proposed Test Case
Send a batch of digest emails and simultaneously trigger a task assignment notification. Verify that the assignment notification is delivered within the expected SLA (under 2 minutes) regardless of digest queue depth. Measure email delivery latency before and after implementing queue separation or prioritization.

## Information Gaps
- Exact email infrastructure architecture (single queue vs. multiple, third-party service vs. self-hosted)
- Whether the digest feature was the only change deployed that week
- Precise delivery latency metrics from server-side logs (reporter can only observe inbox arrival times)
- Whether any email rate limits or throttling are configured on the sending service
