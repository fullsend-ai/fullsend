# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours, likely caused by daily digest emails saturating shared send queue

## Problem
Task assignment notification emails that previously arrived within 1-2 minutes are now arriving 2-4 hours late. The delay is worst in the morning (9-10am) and affects all users. This started approximately one week ago, coinciding with the introduction of a new daily digest email feature.

## Root Cause Hypothesis
The daily digest emails (sent to ~200 users around 9am) and transactional notification emails likely share the same email send queue or rate-limited sending pipeline. The burst of ~200 digest emails at 9am saturates the queue, causing transactional notifications to wait behind them. Afternoon notifications are faster because the digest backlog has cleared by then.

## Reproduction Steps
  1. Assign a task to a user around 9:00-9:30am when daily digest emails are being sent
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Assign a task to a user in the afternoon (e.g., 3pm) after digest processing has completed
  4. Observe that the notification arrives faster but still with some delay

## Environment
Organization with ~200 TaskFlow users. Daily digest email feature recently enabled. Specific email provider/queue system unknown but shared between digest and transactional emails.

## Severity: high

## Impact
All ~200 users in the organization are affected. Team members miss task assignment deadlines because they don't learn about assignments for hours. Morning assignments are most severely impacted.

## Recommended Fix
1. Investigate whether digest and transactional notification emails share a send queue or rate limit. 2. Separate transactional emails (task assignments, etc.) into a high-priority queue distinct from bulk/marketing emails like digests. 3. If using a single email provider with rate limits, implement priority lanes or stagger the digest send over a longer window. 4. Consider sending digests during off-peak hours (e.g., early morning before work starts or overnight).

## Proposed Test Case
Send a batch of 200 digest emails and simultaneously trigger a task assignment notification. Verify the notification email arrives within 2 minutes regardless of digest queue depth. Also verify that after the queue separation fix, morning notifications have the same latency as afternoon notifications.

## Information Gaps
- Exact email provider and queue architecture (developer-side investigation)
- Whether the digest feature was deployed exactly one week ago (deployments/changelogs can confirm)
- Actual email sending rate limits in place
- Whether any email delivery errors or bounces are occurring alongside the delays
