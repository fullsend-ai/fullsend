# Triage Summary

**Title:** Assignment email notifications delayed 2-4 hours, likely caused by new daily digest feature saturating email pipeline

## Problem
Task assignment notifications that previously arrived within 1-2 minutes are now arriving 2-4 hours late for all users. The delays are worst in the morning (9-10am) and somewhat better in the afternoon. This started approximately one week ago, coinciding with the rollout of a new daily digest email feature. Team members are missing deadlines because they don't learn about task assignments in time.

## Root Cause Hypothesis
The new daily digest feature, which sends batch summary emails around 9am, is likely overwhelming the shared email sending infrastructure (e.g., saturating SMTP rate limits, flooding a shared send queue, or consuming API quota on the email provider). Assignment notifications get queued behind the digest batch and drain slowly. Morning delays are worst because they coincide with the digest send window; afternoon notifications are faster because the backlog has partially cleared.

## Reproduction Steps
  1. Assign a task to a user in the morning around 9-10am
  2. Note the timestamp of the assignment
  3. Observe the email delivery timestamp — expect 2-4 hour delay
  4. Repeat in the afternoon — expect shorter but still noticeable delay
  5. Compare against email queue depth and digest send job timing

## Environment
Affects all users across multiple teams. No recent changes to customer-side email infrastructure. TaskFlow deployed a new daily digest email feature approximately one week ago.

## Severity: high

## Impact
All users receiving task assignment notifications are affected. Team members are missing deadlines because they are unaware of new assignments for hours. Multiple team leads have independently reported the issue.

## Recommended Fix
1. Investigate the email sending pipeline for contention between digest batch jobs and transactional notifications. 2. Check email queue depth and processing rate around 9am vs. other times. 3. Prioritize transactional notifications (assignment, deadline) over batch digest emails in the send queue — or use a separate queue/sending channel for each. 4. Review rate limits on the email provider (e.g., SES, SendGrid) to confirm the digest volume isn't exceeding allowed send rates. 5. Consider staggering or throttling digest email sends to avoid a single burst.

## Proposed Test Case
With the digest feature active: assign a task at 9:05am (peak digest window) and verify the notification email arrives within 2 minutes. Repeat with the digest send job disabled to confirm the baseline is restored. Also verify that digest emails themselves still send successfully after the fix.

## Information Gaps
- Exact architecture of the email sending pipeline (shared queue vs. separate channels)
- Volume of digest emails being sent in the morning batch
- Email provider rate limits and whether they are being hit
- Whether any error logs or retry storms appear in the notification service around 9am
