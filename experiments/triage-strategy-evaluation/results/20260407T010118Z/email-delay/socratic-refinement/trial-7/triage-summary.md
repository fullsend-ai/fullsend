# Triage Summary

**Title:** Daily digest email feature is choking the email queue, delaying task assignment notifications by 2-4 hours

## Problem
Since approximately one week ago, task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours in the morning and 20-30 minutes in the afternoon. Multiple users across the team are affected, causing missed deadlines due to late awareness of task assignments.

## Root Cause Hypothesis
A new daily digest email feature, launched roughly one week ago, is sending a large volume of emails around 9am each day. This is saturating the email sending queue or hitting rate limits with the email provider, causing task notification emails to back up behind the digest batch. The queue gradually drains through the day, which explains why morning notifications are delayed hours while afternoon notifications are only slightly delayed.

## Reproduction Steps
  1. Wait for the daily digest email to be sent (~9am)
  2. Assign a task to a user shortly after (e.g., 9:15am)
  3. Observe that the assignment notification email does not arrive for 2-3 hours
  4. Assign another task in the afternoon (e.g., 3pm)
  5. Observe that this notification arrives within 20-30 minutes
  6. Compare with expected delivery time of 1-2 minutes

## Environment
System-wide issue affecting multiple team members. Not specific to any one account, email provider, or device. Correlates with the introduction of a new daily digest email feature approximately one week ago.

## Severity: high

## Impact
All users receiving task assignment notifications are affected. Team members are missing deadlines because they learn of task assignments hours after they are made. Morning assignments are most severely impacted.

## Recommended Fix
Investigate the email sending infrastructure: (1) Check whether the daily digest and transactional task notifications share the same email queue or sending pipeline. (2) If so, separate them — digest emails should use a lower-priority queue or be rate-limited so they don't starve transactional notifications. (3) Consider sending digests through a separate sending pathway or scheduling them during off-peak hours. (4) Review email provider rate limits to ensure the digest volume isn't triggering throttling that affects all outbound email.

## Proposed Test Case
After implementing the fix, send a batch of digest emails and immediately trigger a task assignment notification. Verify the task notification arrives within the expected 1-2 minute window regardless of digest volume. Additionally, load-test by simulating digest sends for the full user base and confirming transactional notification latency stays under an acceptable threshold (e.g., < 2 minutes).

## Information Gaps
- Exact number of users receiving the daily digest (determines queue pressure)
- Whether the email infrastructure uses a single queue or separate queues for different email types
- Email provider and any rate-limiting configuration in place
- Server-side email queue metrics or logs from the past week that could confirm the backlog hypothesis
