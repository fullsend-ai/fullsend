# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours due to shared queue contention with daily digest emails

## Problem
Since the introduction of a daily digest email feature approximately one week ago, task assignment notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours in the morning and 30-60 minutes in the afternoon. This causes users to miss task deadlines because they are unaware of assignments.

## Root Cause Hypothesis
The new daily digest email feature, which sends to ~200 users around 9am, shares the same email queue as transactional task-assignment notifications. The digest batch floods the queue each morning, causing task notifications to wait behind the digest backlog. The queue drains over several hours, explaining why morning delays are worst (2-4 hours) and afternoon delays are shorter (30-60 minutes).

## Reproduction Steps
  1. Wait for 9am when daily digest emails are sent to ~200 users
  2. Assign a task to a user shortly after 9am
  3. Observe that the assignment notification email arrives 2-4 hours late
  4. Repeat the assignment in the afternoon and observe a shorter delay (30-60 minutes)

## Environment
TaskFlow instance with approximately 200 users. Daily digest feature was introduced about one week ago. Digest and transactional notifications appear to share the same email sending infrastructure (same sender address).

## Severity: high

## Impact
Widespread across the organization (~200 users). Users miss task deadlines because assignment notifications arrive hours late. Morning assignments are most severely affected, which is a high-activity period.

## Recommended Fix
1. Verify whether digest and transactional emails share the same queue (check email service config and queue metrics). 2. If confirmed, separate transactional notifications into a high-priority queue distinct from bulk/digest emails. 3. Alternatively, throttle or stagger digest sends so they don't flood the queue in a single burst. 4. Consider sending digests during off-peak hours (e.g., 7am or 6pm) as a short-term mitigation.

## Proposed Test Case
Send a batch of 200 digest-equivalent emails, then immediately enqueue a task assignment notification. Verify the assignment notification is delivered within 2 minutes regardless of digest queue depth. Test both with shared and separated queues to confirm the fix.

## Information Gaps
- Exact email service and queue implementation (e.g., SES, SendGrid, self-hosted SMTP with a queue table)
- Whether there are other automated bulk emails besides the daily digest
- Server-side queue metrics confirming queue depth spike at 9am
