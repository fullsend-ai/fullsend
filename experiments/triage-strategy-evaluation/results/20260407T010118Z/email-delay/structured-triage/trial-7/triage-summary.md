# Triage Summary

**Title:** Email notifications delayed 2-4 hours after v2.3.1 update, worst during morning hours

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays began approximately one week ago, coinciding with a product update to v2.3.1 that also introduced daily digest emails. Delays are worst for notifications sent around 9-10am and improve in the afternoon. Notifications arrive as individual emails (not bundled into the digest), and the user's notification settings are configured for immediate delivery.

## Root Cause Hypothesis
The v2.3.1 update likely introduced a daily digest/batch email processing job that is interfering with the immediate notification queue. The morning spike suggests the digest job runs in the morning and either monopolizes the email sending worker, clogs the SMTP connection pool, or introduces a queue backlog that individual notifications get stuck behind. As the digest backlog clears through the day, afternoon notifications process faster.

## Reproduction Steps
  1. Use TaskFlow v2.3.1 with email notifications set to 'send immediately'
  2. Assign a task to a user in the morning (around 9-10am)
  3. Note the timestamp of the assignment in TaskFlow
  4. Check the recipient's inbox and note when the notification email arrives
  5. Observe a 2-4 hour delay between assignment and email delivery
  6. Repeat in the afternoon and observe a shorter (but still present) delay

## Environment
TaskFlow v2.3.1 (cloud or self-hosted not confirmed), email delivered via company SMTP relay, notification settings set to immediate delivery, daily digest feature active (likely enabled by the update)

## Severity: high

## Impact
Users are missing task assignment deadlines because they don't learn about assignments for hours. Affects all users relying on email notifications for task assignments, with morning assignments being most severely delayed.

## Recommended Fix
Investigate the email sending pipeline introduced or modified in v2.3.1, specifically: (1) Check if the daily digest job shares a queue or worker pool with immediate notifications and is starving them. (2) Check SMTP connection handling — the digest may be holding connections or hitting rate limits that delay individual sends. (3) Consider separating the digest email queue from the immediate notification queue so they don't compete for resources. (4) Check email worker logs for queue depth and processing times around 9-10am vs afternoon.

## Proposed Test Case
Send a task assignment notification at 9am (peak digest processing time) and verify it is delivered within 2 minutes. Confirm that the digest job running concurrently does not delay individual notification delivery. Measure queue depth and delivery latency for individual notifications before and after separating the queues.

## Information Gaps
- Exact SMTP relay configuration and any rate limits imposed by the company mail server
- Whether other users on the same TaskFlow instance experience the same delays
- Server-side email queue logs showing when notifications are enqueued vs. when they are handed off to SMTP
- Whether the digest feature was auto-enabled by the v2.3.1 update or manually enabled
