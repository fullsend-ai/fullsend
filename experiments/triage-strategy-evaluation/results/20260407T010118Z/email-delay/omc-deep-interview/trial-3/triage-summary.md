# Triage Summary

**Title:** Transactional email notifications delayed 2-4 hours, likely caused by new daily digest emails saturating the send queue

## Problem
Since approximately one week ago, task assignment and due date reminder emails are arriving 2-4 hours late. The delay is worst in the morning (9-10am) and less severe in the afternoon. Email headers confirm the delay occurs before sending (TaskFlow holds the email, not an external mail pipeline). The issue is team-wide, not user-specific.

## Root Cause Hypothesis
A new daily digest/marketing email feature was rolled out approximately one week ago. These digest emails are likely sent in bulk during the morning, saturating a shared email send queue or exhausting a rate limit with the email provider. Transactional notifications (task assignments, due date reminders) sit behind the digest batch in the queue, causing multi-hour delays. The afternoon improvement aligns with the digest batch having cleared by then.

## Reproduction Steps
  1. Assign a task to a user at approximately 9:00 AM (when daily digests are being sent)
  2. Note the timestamp of the assignment in the application
  3. Observe when the notification email is actually sent (check email send logs or queue)
  4. Compare with the same action performed in the afternoon to confirm the timing differential
  5. Check the email queue depth and processing rate during morning digest sends

## Environment
Affects the reporter's entire team. Standard email setup with no third-party filtering (no Mimecast, Proofpoint, etc.). Issue began approximately one week ago, coinciding with the introduction of daily digest emails.

## Severity: high

## Impact
Team-wide. Users are missing task assignment deadlines because they don't learn about assignments until hours after they're made. Multiple notification types affected (assignments confirmed, due date reminders likely, comment notifications unknown).

## Recommended Fix
1. Investigate whether transactional notifications and digest/marketing emails share the same send queue or email provider rate limit. 2. Separate transactional emails (assignments, reminders) into a high-priority queue distinct from bulk digest sends. 3. If using a single email provider API key, check if the digest volume is hitting rate limits that block transactional sends. 4. Consider sending digests during off-peak hours or throttling digest throughput to leave headroom for transactional email.

## Proposed Test Case
Send a batch of digest emails (simulating the daily digest volume) and simultaneously trigger a task assignment notification. Verify that the assignment notification is sent within 2 minutes regardless of digest queue depth. This confirms that queue separation or prioritization is working correctly.

## Information Gaps
- Exact volume of daily digest emails being sent (how many recipients per batch)
- Whether comment notifications are also delayed (reporter was unsure)
- Which email sending infrastructure TaskFlow uses (in-house SMTP, SendGrid, SES, etc.)
- Whether the email queue is shared or already segmented by email type
