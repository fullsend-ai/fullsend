# Triage Summary

**Title:** Email notifications delayed 2-4 hours after v2.3.1 update, likely caused by daily digest feature saturating or blocking the mail queue

## Problem
Since approximately one week ago (coinciding with a product update to v2.3.1), task assignment email notifications are delayed by 2-4 hours when sent in the morning, and 20-30 minutes in the afternoon. Previously notifications arrived within 1-2 minutes. This is causing users to miss task deadlines.

## Root Cause Hypothesis
The daily digest feature introduced in the recent update is likely saturating or blocking the email sending queue when it runs around 9am. The digest job may be processing a large batch of emails synchronously on the same queue as individual notifications, starving real-time notifications of throughput. As the digest backlog clears through the morning, individual notification latency gradually improves, explaining why afternoon notifications are only mildly delayed.

## Reproduction Steps
  1. Set up a self-hosted TaskFlow v2.3.1 instance with daily digest emails enabled
  2. Configure email delivery through an SMTP relay
  3. Assign a task to a user at approximately 9:15am (shortly after the digest is scheduled)
  4. Observe that the assignment notification email is delayed by 2+ hours
  5. Assign another task to the same user at approximately 2:00pm
  6. Observe that this notification arrives within 20-30 minutes

## Environment
TaskFlow v2.3.1, self-hosted, corporate SMTP relay, Outlook email clients

## Severity: high

## Impact
All users receiving email notifications are affected. Morning notifications are severely delayed (2-4 hours), causing users to miss task assignment deadlines. This affects team coordination and task handoff workflows.

## Recommended Fix
Investigate the email sending architecture in v2.3.1, specifically: (1) Check whether the daily digest job and individual notifications share the same mail queue or worker pool. (2) Inspect the mail queue depth and processing rate around 9am when the digest runs. (3) If they share a queue, separate real-time notifications onto a higher-priority queue or dedicated worker. (4) Consider making the digest job rate-limited or sending digests in smaller batches to avoid starving the notification pipeline.

## Proposed Test Case
With daily digests enabled, trigger a digest send for N users and simultaneously enqueue an individual task assignment notification. Assert that the individual notification is delivered within the expected SLA (e.g., under 2 minutes) regardless of digest queue depth. Additionally, add a queue-depth monitoring test that alerts when the mail queue exceeds a threshold.

## Information Gaps
- Server-side mail queue logs or TaskFlow application logs from the morning period would confirm the queue saturation hypothesis
- Whether other self-hosted v2.3.1 users with daily digests enabled experience the same delay pattern
- The exact number of users/tasks generating digest emails (queue volume)
