# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours, likely due to digest email feature saturating the email queue

## Problem
Task assignment notification emails that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and improve in the afternoon. Multiple team members are affected, causing missed deadlines due to late awareness of task assignments.

## Root Cause Hypothesis
The recently launched daily digest/marketing summary email feature sends bulk emails to all users around 9am. These bulk sends are likely sharing the same email queue or sending infrastructure as transactional task notification emails, causing the transactional emails to back up behind the large batch of digest emails. As the digest queue drains through the morning, task notifications gradually catch up, explaining the afternoon improvement.

## Reproduction Steps
  1. Assign a task to a user via TaskFlow around 9:00-9:30am (during or shortly after digest email send window)
  2. Note the timestamp of the assignment in TaskFlow
  3. Monitor the recipient's inbox for the notification email
  4. Observe a delay of 2-4 hours between assignment and email arrival
  5. Repeat the assignment around 2-3pm and observe a shorter (but still abnormal) delay

## Environment
Production environment; affects multiple users across the team; issue began approximately one week ago, coinciding with the launch of the daily digest email feature

## Severity: high

## Impact
Multiple team members are missing task assignment deadlines because notifications arrive hours late. Directly affects team productivity and deadline adherence. Morning assignments are most severely impacted.

## Recommended Fix
1. Investigate whether digest emails and transactional notification emails share the same email queue/sending pipeline. 2. If so, separate them — transactional notifications should use a dedicated high-priority queue that is not blocked by bulk sends. 3. Consider rate-limiting or staggering the digest email batch to avoid saturating the email provider. 4. Check email provider rate limits and whether the digest volume is hitting throttling thresholds.

## Proposed Test Case
After implementing the fix, send a batch of digest emails and simultaneously trigger a task assignment notification. Verify the task notification arrives within 2 minutes regardless of digest send volume. Run this test during the 9am window to confirm the morning congestion pattern is resolved.

## Information Gaps
- Exact launch date of the digest email feature (to correlate with the start of delays in email logs)
- Total number of users receiving digest emails (to estimate queue volume)
- Whether the email infrastructure uses a single queue or separate queues for different email types
- Whether any notifications are being dropped entirely rather than just delayed
