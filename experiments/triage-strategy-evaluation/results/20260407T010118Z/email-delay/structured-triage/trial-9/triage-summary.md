# Triage Summary

**Title:** Email notifications delayed 2-4 hours, correlating with new daily digest feature rollout

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is worst for notifications sent around 9-10am and somewhat better in the afternoon. This affects the reporter's entire team (~200 users) and is causing missed deadlines due to late awareness of task assignments.

## Root Cause Hypothesis
The new daily digest email feature (introduced ~1 week ago, matching the onset of delays) is likely competing with real-time notification delivery. The digest is probably processed via the same email sending queue/worker, and its batch job — likely scheduled in the morning — saturates the queue or rate limit, causing transactional notifications to back up. The morning spike aligns with a digest generation/send window, and afternoon improvement aligns with the digest batch completing.

## Reproduction Steps
  1. Assign a task to a user in TaskFlow v2.3.1 around 9-10am
  2. Observe that the email notification arrives 2-4 hours later rather than within 1-2 minutes
  3. Repeat the assignment in the afternoon and observe a shorter but still noticeable delay

## Environment
TaskFlow v2.3.1, email-based notifications, ~200-user team, no recent changes on the customer's end (no new email filters or provider changes)

## Severity: high

## Impact
All ~200 users on the team are affected. Delayed notifications cause missed deadlines because users don't learn about task assignments in time to act on them.

## Recommended Fix
Investigate the email sending infrastructure for queue contention between the new daily digest feature and real-time transactional notifications. Likely fixes: (1) Use separate queues or workers for digest batch emails vs. real-time notifications, with real-time taking priority. (2) Check if the digest job is hitting email provider rate limits that cause transactional emails to be throttled. (3) Review the digest job's scheduling — if it runs at 9am and processes digests for many users, it would explain the morning bottleneck.

## Proposed Test Case
With the digest feature active, assign a task at 9am (peak digest time) and verify the notification arrives within the expected SLA (e.g., under 2 minutes). Additionally, temporarily disable the digest feature and confirm that notification latency returns to normal to validate the root cause.

## Information Gaps
- Server-side email queue metrics and logs to confirm queue depth during morning hours
- Exact schedule and implementation details of the daily digest feature
- Whether the email provider has rate-limiting that the digest volume might be triggering
