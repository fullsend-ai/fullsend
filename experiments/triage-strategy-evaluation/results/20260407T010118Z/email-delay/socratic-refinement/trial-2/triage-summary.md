# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours, likely caused by daily digest email batch saturating send pipeline

## Problem
Task assignment notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and improve through the afternoon. This started approximately one week ago, coinciding with the introduction of daily digest/marketing summary emails sent to all users at 9am.

## Root Cause Hypothesis
The daily digest email batch (sent to all ~200 users at ~9am) is being processed through the same email sending pipeline as transactional assignment notifications. The burst of ~200 digest emails saturates the queue or hits rate limits on the email provider, causing assignment notifications to back up behind them. As the digest batch drains, afternoon notifications experience shorter delays.

## Reproduction Steps
  1. Assign a task to a user at approximately 9:00-9:30am (when digest emails are being sent)
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Assign a task to a user in the late afternoon (after digest queue has drained)
  4. Observe that the notification arrives faster but still with some delay

## Environment
TaskFlow instance with approximately 200 users, daily digest emails enabled organization-wide, sending at ~9am

## Severity: high

## Impact
All ~200 users are affected. Delayed assignment notifications cause people to miss deadlines because they are unaware of new task assignments for hours. Morning assignments are most impacted.

## Recommended Fix
Investigate the email sending architecture: (1) Determine whether digest and transactional emails share the same queue/worker pool. If so, separate them into distinct queues with transactional notifications given higher priority. (2) Check for rate limits on the email provider that the digest batch may be exhausting. (3) Consider scheduling digest emails during off-peak hours or throttling them to leave capacity for transactional sends. (4) As an immediate mitigation, consider sending digests in smaller batches spread over a longer window rather than all at once.

## Proposed Test Case
Send a batch of 200 digest emails and simultaneously trigger 5 assignment notifications. Verify that assignment notifications are delivered within the expected SLA (e.g., under 2 minutes) regardless of digest batch volume. Test both with shared and separated queues to confirm the fix.

## Information Gaps
- Exact email infrastructure details (shared queue, provider, rate limits) — these are internal system details the reporter would not know
- Whether other transactional notification types (e.g., due date reminders, comment notifications) are also delayed
- Whether the digest feature was deliberately launched a week ago or appeared unexpectedly (could indicate a feature flag or deployment)
