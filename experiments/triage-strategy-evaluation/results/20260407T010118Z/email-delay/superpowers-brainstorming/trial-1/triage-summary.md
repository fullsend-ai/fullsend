# Triage Summary

**Title:** Email notifications delayed 2-4 hours, likely caused by new daily digest batch saturating mail queue

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst for notifications sent around 9-10am and improve in the afternoon. The issue began approximately one week ago, coinciding with the launch of a new daily digest email feature. The problem is widespread, confirmed across multiple team members in a ~200-person organization.

## Root Cause Hypothesis
The new daily digest email, sent to ~200 users around 9am, is saturating the mail queue or hitting rate limits on the email sending service. Transactional notifications (task assignments) are queued behind the bulk digest batch rather than being prioritized, causing a cascading delay that is worst in the morning and gradually clears through the afternoon as the queue drains.

## Reproduction Steps
  1. Assign a task to a user around 9:00-9:30am (when digest emails are being sent)
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Repeat the same assignment around 2-3pm and observe a shorter (but still present) delay
  4. Compare against email sending logs to confirm the digest batch is queued immediately before the delayed notifications

## Environment
TaskFlow instance with ~200 users. Daily digest email feature recently enabled (approximately 1 week ago). Email provider/SMTP configuration unknown to reporter — needs developer investigation.

## Severity: high

## Impact
All ~200 users in the organization are affected. Delayed task assignment notifications are causing people to miss deadlines because they don't learn about new assignments for hours. Morning assignments are most impacted.

## Recommended Fix
1. Immediately investigate mail queue logs to confirm digest emails are contending with transactional notifications. 2. Separate transactional emails (task assignments) from bulk emails (digests) — either use separate queues, a priority system, or separate sending infrastructure. 3. If using a third-party email service, check if rate limits are being hit by the digest batch. 4. Consider sending digests during off-peak hours (e.g., 6am or overnight) or throttling digest sends to avoid queue saturation. 5. Long-term: ensure transactional emails always bypass or take priority over bulk sends.

## Proposed Test Case
After implementing the fix, send a digest batch to all 200 users and simultaneously trigger a task assignment notification. Verify the assignment notification arrives within 2 minutes regardless of digest queue state. Repeat at 9am to confirm the morning delay pattern is resolved.

## Information Gaps
- Which email sending service or SMTP infrastructure TaskFlow uses (reporter lacks visibility)
- Whether the digest feature was intentionally launched or is in beta/rollout
- Exact mail queue architecture and whether it already has priority separation
- Whether any email rate limits or throttling were triggered (visible in sending service logs)
