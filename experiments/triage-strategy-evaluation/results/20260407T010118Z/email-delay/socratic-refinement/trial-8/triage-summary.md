# Triage Summary

**Title:** Task notification emails delayed 2-4 hours due to daily digest email queue contention

## Problem
Task assignment email notifications, which previously arrived within 1-2 minutes, are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and improve by afternoon. The issue began approximately one week ago, coinciding with the introduction of a new daily digest email feature. The problem affects all team members, not just one user.

## Root Cause Hypothesis
The daily digest emails (sent to ~200 users around 9am) are being processed through the same email sending queue or worker as transactional task-assignment notifications. The burst of ~200 digest emails saturates the queue each morning, causing task notifications to wait behind the digest batch. As the digest backlog clears through the day, notification latency improves.

## Reproduction Steps
  1. Observe the email sending queue around 9am when daily digests are dispatched
  2. Assign a task to a user at ~9:15am, shortly after digest sending begins
  3. Measure time between task assignment and notification email delivery
  4. Compare with the same test at ~3pm when the queue should be idle
  5. Confirm the morning delay is significantly worse than the afternoon delay

## Environment
TaskFlow instance with approximately 200 users, daily digest email feature recently enabled, shared email sending infrastructure

## Severity: high

## Impact
All users (~200) on the instance are affected. Delayed task assignment notifications cause team members to miss or be late to deadlines, directly impacting project delivery and team coordination.

## Recommended Fix
Investigate whether digest emails and transactional notifications share the same email queue/worker. The fix is likely one of: (1) separate the queues so transactional notifications have priority over bulk digest sends, (2) throttle or stagger the digest send over a longer window to avoid a burst, or (3) use a priority mechanism in the existing queue that sends transactional emails before bulk emails. Option 1 is the most robust long-term solution.

## Proposed Test Case
Send a batch of 200 digest emails and simultaneously trigger a task assignment notification. Verify that the task notification is delivered within the expected SLA (e.g., under 2 minutes) regardless of digest queue depth. Repeat the test during the digest send window (9am) to confirm priority handling works under real conditions.

## Information Gaps
- Exact email infrastructure details (single queue, single worker, third-party service vs. self-hosted SMTP)
- Whether the digest feature sends 200 individual emails or uses a batch/bulk API
- Whether there are any email rate limits imposed by the sending provider that could compound the issue
