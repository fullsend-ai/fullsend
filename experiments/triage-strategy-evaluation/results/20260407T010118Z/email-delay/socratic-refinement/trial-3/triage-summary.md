# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours, likely caused by daily digest email queue saturation

## Problem
Since approximately one week ago, task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and improve in the afternoon. This is team-wide and causing missed deadlines because assignees don't learn about tasks promptly.

## Root Cause Hypothesis
A recently introduced daily digest email feature is sending bulk emails around 9am, saturating the email sending queue or hitting rate limits with the email provider. Transactional notifications (task assignments) are queued behind the digest batch and must wait until the bulk send completes or the rate limit resets. The afternoon improvement occurs because the digest backlog has cleared by then.

## Reproduction Steps
  1. Assign a task to a user in the morning around 9-10am, after the daily digest has been sent
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Assign a task in the afternoon (e.g., 3-4pm) and observe a shorter but still present delay
  4. Check the email sending queue/logs to confirm digest emails and assignment notifications share the same queue

## Environment
Affects all team members. Started approximately one week ago, coinciding with the rollout of the daily digest email feature. No other changes to team size, workflows, or integrations.

## Severity: high

## Impact
All users receiving task assignments are affected. Delayed notifications cause team members to miss or be late on assigned tasks, directly impacting deadline compliance across the team.

## Recommended Fix
Investigate the email sending infrastructure for queue contention between bulk digest sends and transactional notifications. Likely fixes include: (1) separate the transactional notification queue from the bulk/marketing email queue so they don't compete for the same throughput, (2) prioritize transactional emails over digest emails, or (3) throttle/stagger the digest send to avoid saturating the pipeline. Check email provider rate limits and sending logs around 9am to confirm the hypothesis.

## Proposed Test Case
After implementing the fix, send a daily digest to the full recipient list. Immediately after the digest begins sending, trigger a task assignment notification. Verify the assignment notification arrives within 2 minutes regardless of the digest send volume or time of day. Repeat at 9am to confirm morning performance matches afternoon performance.

## Information Gaps
- Exact email infrastructure (shared queue, provider rate limits, queue depth)
- Number of digest recipients (scale of the bulk send)
- Whether the digest feature was an intentional product rollout or an A/B test
- Whether other transactional emails (e.g., password resets, comment notifications) are also delayed or only task assignments
