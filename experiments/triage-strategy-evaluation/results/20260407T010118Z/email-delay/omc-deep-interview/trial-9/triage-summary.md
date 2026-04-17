# Triage Summary

**Title:** Transactional email notifications delayed 2-4 hours, likely due to daily digest job saturating email queue

## Problem
All email notifications (task assignments, due-date reminders, etc.) are arriving 2-4 hours late for the past week. The delay is worst in the morning (9-10am) and affects the entire team. This is causing missed deadlines because users don't learn about task assignments promptly.

## Root Cause Hypothesis
A recently launched daily digest/summary email feature sends bulk emails around 9am. This likely shares the same email-sending queue or rate-limited provider pipeline as transactional notifications. The digest job floods the queue each morning, pushing time-sensitive notifications to the back. Afternoon notifications are faster because the digest backlog has cleared by then. The digest emails themselves arrive on time because they are enqueued first.

## Reproduction Steps
  1. Assign a task to a user at approximately 9:00-9:15am, shortly after the daily digest job runs
  2. Observe the time gap between the assignment timestamp in the database/dashboard and when the notification email is actually sent (check email queue logs)
  3. Compare with a task assigned in the afternoon (e.g., 2pm) and observe the shorter delay
  4. Check the email queue depth and processing times around 9am vs. other times of day
  5. Disable or defer the digest job and verify that morning notification latency returns to normal

## Environment
Server-side issue affecting all users. Not specific to any email provider, client, or account. Team-wide across all email notification types.

## Severity: high

## Impact
All users receiving email notifications are affected. Team members are missing task assignment deadlines because they learn about assignments hours late. Morning workflows are most impacted. This undermines the core utility of task-assignment notifications.

## Recommended Fix
1. Investigate the email-sending infrastructure — confirm whether digest and transactional emails share a queue or rate limit. 2. Separate transactional notifications into a high-priority queue distinct from bulk digest sends, or implement priority levels within the existing queue. 3. If using an external email provider, check whether the digest volume is hitting API rate limits that delay subsequent sends. 4. Consider scheduling the digest job during off-peak hours (e.g., 7am or overnight) so it doesn't compete with the morning burst of transactional notifications.

## Proposed Test Case
Send a batch of digest emails (simulating the daily digest volume), then immediately enqueue a transactional notification. Assert that the transactional notification is sent within 2 minutes regardless of digest queue depth. This validates that priority separation is working correctly.

## Information Gaps
- Exact date the digest feature was deployed (verifiable from deployment logs)
- Email queue architecture — single queue vs. separate queues, provider rate limits
- Email header timestamps from a delayed notification (reporter will attempt to provide, but developer can also check send logs directly)
- Exact volume of digest emails sent per run and whether it exceeds provider rate limits
