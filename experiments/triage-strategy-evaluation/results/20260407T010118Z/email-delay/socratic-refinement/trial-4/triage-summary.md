# Triage Summary

**Title:** Email notification delays (2-4h) caused by likely queue contention with daily digest feature

## Problem
Task assignment email notifications, previously delivered within 1-2 minutes, have been delayed by 2-4 hours for the past week. Delays are worst for notifications sent around 9-10am and less severe in the afternoon. Delayed notifications arrive in clusters, suggesting queue backlog and batch processing rather than individual delivery failures.

## Root Cause Hypothesis
A daily digest email feature launched approximately one week ago is likely saturating a shared email sending queue or rate limit. The digest job probably runs in the morning, consuming queue capacity and causing transactional notifications to back up. As the digest completes, the queued notifications drain in clusters. Afternoon notifications are faster because the digest backlog has cleared by then.

## Reproduction Steps
  1. Assign a task to a user around 9-10am when the daily digest is expected to be sending
  2. Observe that the assignment notification email is delayed by 2-4 hours
  3. Repeat in the afternoon and observe shorter (but still present) delays
  4. Check the email sending queue/logs to confirm backlog during digest send window

## Environment
Production environment; affects task assignment notification emails. Reporter's team size and workflow unchanged. Issue began approximately one week ago, coinciding with daily digest feature rollout.

## Severity: high

## Impact
Users are missing task assignment deadlines because they learn about assignments 2-4 hours late. Affects any user relying on email notifications for task awareness, with morning assignments being most impacted.

## Recommended Fix
1. Confirm hypothesis by checking email queue depth and processing times, correlating with digest send schedule. 2. Separate transactional notifications (task assignments) from bulk email (digests) into distinct queues or priority tiers so bulk sends cannot starve time-sensitive notifications. 3. If using a single email provider rate limit, implement priority-based sending where transactional emails always take precedence over batch/marketing emails.

## Proposed Test Case
Send a task assignment notification during peak digest processing time and verify it arrives within the expected SLA (e.g., under 2 minutes), independent of digest queue depth. Additionally, load-test the email pipeline with a simulated digest batch and concurrent transactional sends to confirm no cross-contamination of delivery times.

## Information Gaps
- Exact send time of the daily digest job (confirmable from server config/logs)
- Whether the email pipeline uses a single shared queue or separate channels (confirmable from architecture)
- How many users/organizations are affected beyond this reporter (confirmable from delivery metrics)
- Which email service provider or sending infrastructure is in use
