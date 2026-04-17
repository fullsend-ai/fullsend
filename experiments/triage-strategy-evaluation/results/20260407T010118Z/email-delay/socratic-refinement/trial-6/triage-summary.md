# Triage Summary

**Title:** Email notifications delayed 2-4 hours since daily digest feature launch

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours. The delays are worst for notifications sent in the morning (9-10am) and affect the entire team. This started approximately one week ago, coinciding with the launch of a new daily digest/summary email feature in TaskFlow.

## Root Cause Hypothesis
The new daily digest feature likely shares the same email sending queue or infrastructure as transactional notifications. When the digest emails are generated and sent each morning, they flood the queue and push time-sensitive task assignment notifications behind a large batch of lower-priority digest emails. This explains why morning delays are worst (digest sends in the morning) and afternoon notifications are faster (digest backlog has cleared).

## Reproduction Steps
  1. Assign a task to a user in the morning (around 9-10am) when the daily digest emails are being sent
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Repeat the same assignment in the afternoon and observe a shorter (but still present) delay
  4. Check the email queue depth and processing times around the morning digest window

## Environment
Affects all team members. Began approximately one week ago with the release of the daily digest feature. Not specific to any one user's settings or email provider.

## Severity: high

## Impact
Entire team is missing task assignment deadlines because they don't learn about assignments for hours. Directly affects team productivity and deadline adherence.

## Recommended Fix
Investigate the email sending infrastructure for queue contention between digest emails and transactional notifications. Likely fixes: (1) separate the transactional notification queue from the bulk digest queue so they don't compete, (2) prioritize transactional emails over digest emails in the queue, or (3) rate-limit or schedule digest sends to avoid flooding the queue during peak notification hours.

## Proposed Test Case
Send a task assignment notification while a daily digest batch is actively being processed. Verify the assignment notification is delivered within 2 minutes regardless of digest queue depth.

## Information Gaps
- Exact time the daily digest emails are sent each morning
- Whether the email system uses a single queue or separate queues for different email types
- Exact number of users/projects receiving digest emails (scale of the digest send)
