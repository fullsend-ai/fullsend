# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours since daily digest feature was introduced

## Problem
Individual task assignment notification emails, which previously arrived within 1-2 minutes, are now arriving 2-4 hours late. The delays are worst for notifications sent around 9-10am and somewhat better in the afternoon. This started approximately one week ago, coinciding with the introduction of a new daily project activity digest email feature. The individual assignment notifications are still being sent as separate emails (not folded into the digest), but they are significantly delayed.

## Root Cause Hypothesis
The new daily digest feature most likely shares the same email sending queue or pipeline as real-time assignment notifications. When the digest job runs (likely in the morning), it generates a large batch of digest emails that flood the queue, causing assignment notifications to wait behind them. This explains why morning notifications (sent around the same time the digest job runs) experience the longest delays, while afternoon notifications — sent after the digest backlog has cleared — are faster but still affected by general queue congestion.

## Reproduction Steps
  1. Assign a task to a user in the morning (9-10am) and record the timestamp
  2. Monitor when the assignment notification email is actually delivered
  3. Compare the email queue depth before and after the daily digest job runs
  4. Repeat the assignment in the afternoon and compare delivery latency

## Environment
TaskFlow application with recently deployed daily digest email feature. Specific version and infrastructure details not available from reporter.

## Severity: high

## Impact
All users receiving task assignment notifications are affected. Delayed notifications cause people to miss deadlines because they are unaware of new task assignments for hours. Morning assignments are most impacted, which is likely peak task-assignment time for most teams.

## Recommended Fix
1. Investigate whether the daily digest email job and real-time notifications share the same email queue or sending pipeline. 2. If so, separate them — use a dedicated high-priority queue for real-time notifications and a separate lower-priority queue for batch digest emails, or rate-limit the digest job so it doesn't monopolize the sender. 3. Check the email sending infrastructure for concurrency limits or throttling that the digest volume may be hitting. 4. Consider scheduling the digest job during off-peak hours (e.g., early morning before business hours) to minimize contention.

## Proposed Test Case
After separating the queues (or applying the fix), assign a task during peak morning hours while the digest job is actively running. Verify that the assignment notification email is delivered within the original SLA (1-2 minutes) regardless of digest job activity.

## Information Gaps
- Exact email infrastructure in use (self-hosted SMTP, third-party service like SendGrid/SES, etc.)
- Whether the digest feature was the only change in the deployment that introduced it
- Number of users/projects generating digest emails (scale of the batch job)
- Whether other notification types (e.g., comment notifications, due date reminders) are also delayed
