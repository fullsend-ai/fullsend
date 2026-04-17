# Triage Summary

**Title:** Bulk daily digest email job saturating email queue, delaying transactional task-assignment notifications by 2-4 hours

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst for notifications sent around 9-10am and improve in the afternoon. This started approximately one week ago and affects all team members. Users are missing task deadlines because they don't learn about assignments in time.

## Root Cause Hypothesis
A new daily digest (marketing summary) email feature was deployed approximately one week ago. It fires around 9am and sends to all users on the team. This bulk send is likely sharing the same email queue or rate-limited sending pipeline as transactional notifications. The digest job floods the queue each morning, pushing time-sensitive task-assignment notifications to the back. As the digest backlog drains through the morning, transactional notifications resume normal speed by afternoon.

## Reproduction Steps
  1. Assign a task to a user around 9:00-9:15am (shortly after the daily digest job runs)
  2. Observe that the assignment notification email arrives 2-4 hours late
  3. Repeat the same assignment in the afternoon (e.g., 2-3pm)
  4. Observe that the afternoon notification arrives significantly faster
  5. Compare email queue depth/throughput logs around 9am vs. afternoon to confirm the digest job is the bottleneck

## Environment
Affects all team members. No changes to team size or task volume. Issue coincides with the rollout of a new daily digest email feature approximately one week ago.

## Severity: high

## Impact
All team members are receiving delayed task assignment notifications, causing missed deadlines. The delay window (2-4 hours in the morning) overlaps with the start of the workday when most task assignments happen, maximizing the operational impact.

## Recommended Fix
1. Investigate the email sending architecture — confirm whether the daily digest and transactional notifications share the same queue/pipeline. 2. Separate transactional notifications into a high-priority queue distinct from bulk/marketing sends. 3. As an immediate mitigation, consider moving the digest job to off-peak hours (e.g., 7am or 6am before work starts) or throttling its send rate to leave headroom for transactional emails. 4. Add monitoring/alerting on email queue depth and transactional notification latency.

## Proposed Test Case
After implementing queue separation (or rescheduling the digest), send a task assignment at 9:15am and verify the notification email arrives within 2 minutes. Repeat across multiple days to confirm the morning delay pattern is eliminated. Also verify the daily digest itself still delivers successfully.

## Information Gaps
- Exact email infrastructure in use (third-party service vs. self-hosted, queue implementation)
- Whether the digest job was an intentional feature release or an accidental enablement
- Precise volume of digest emails sent per morning run (team size × digest content)
