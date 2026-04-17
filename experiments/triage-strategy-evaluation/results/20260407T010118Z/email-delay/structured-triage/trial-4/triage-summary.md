# Triage Summary

**Title:** Email notifications delayed 2-4 hours since ~v2.3.1 update, likely related to Daily Digest feature rollout

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. Delays are worse for notifications generated in the morning (9-10am) and somewhat better in the afternoon. The issue began approximately one week ago, coinciding with a product update.

## Root Cause Hypothesis
The recently introduced 'Daily Digest' email feature likely changed the notification pipeline — either by routing all notifications through a batching/digest queue instead of sending them immediately, or by introducing a shared email-sending job whose throughput is saturated by digest processing in the morning. The morning spike aligns with a digest compilation or batch send that could be monopolizing the email worker pool.

## Reproduction Steps
  1. Assign a task to a user in TaskFlow v2.3.1
  2. Note the exact time the assignment is made
  3. Observe when the notification email arrives in the assignee's inbox
  4. Compare the delay for assignments made at ~9-10am versus ~2-3pm

## Environment
TaskFlow v2.3.1 (likely cloud-hosted with recent update), recipient email via corporate Exchange server. Exchange is not the bottleneck — non-TaskFlow emails arrive promptly.

## Severity: high

## Impact
Team members are missing task assignment deadlines because they don't learn about assignments for hours. Affects all users relying on email notifications, with morning assignments most severely impacted.

## Recommended Fix
Investigate the notification pipeline changes introduced alongside the Daily Digest feature. Specifically: (1) Check whether immediate notifications are now being routed through the digest/batch queue instead of being sent inline. (2) Examine the email worker pool or queue for contention between digest sends and real-time notifications. (3) Verify that the notification_created_at vs. notification_sent_at timestamps in the delivery log confirm a server-side queuing delay. If digest processing is starving real-time sends, separate the queues or prioritize immediate notifications over digest compilation.

## Proposed Test Case
After the fix, assign a task at 9:30am and verify the notification email is received within 2 minutes. Repeat at 2:30pm. Confirm that Daily Digest emails still send correctly on their schedule without interfering with real-time notification delivery.

## Information Gaps
- Exact product update or changelog for the release that coincided with the delays
- Server-side notification queue and delivery logs (reporter lacks admin access)
- Whether other users on the same team experience identical delays
- Whether the Daily Digest feature can be disabled per-user to test if it resolves the delay
