# Triage Summary

**Title:** Email notifications delayed 2-4 hours due to daily digest batch job saturating email queue

## Problem
Transactional email notifications (task assignments, etc.) are arriving 2-4 hours late for all users. The delay is worst in the morning (9-10am) and improves in the afternoon. This started approximately one week ago, coinciding with the launch of a new daily digest email feature. In-app notifications are unaffected.

## Root Cause Hypothesis
The new daily digest feature sends a batch of marketing/summary emails to all ~200 users around 9am through the same email pipeline used for transactional notifications. This flood of digest emails saturates the queue, causing transactional emails (task assignments) to wait behind them. As the digest backlog drains through the day, afternoon transactional emails experience shorter delays.

## Reproduction Steps
  1. Assign a task to a user around 9-10am (when daily digest emails are being sent)
  2. Observe that the in-app notification appears promptly
  3. Observe that the email notification arrives 2-4 hours later
  4. Repeat the same assignment in the afternoon and observe a shorter (but still present) delay

## Environment
Organization with ~200 users on TaskFlow. No Slack integration in use. Users primarily rely on email notifications. Daily digest feature launched approximately one week ago.

## Severity: high

## Impact
All users in the organization are affected. Users who rely on email notifications (rather than keeping the app open) are missing task assignment deadlines because they don't learn about assignments for hours. This directly impacts team productivity and deadline adherence.

## Recommended Fix
Separate transactional emails (task assignments, mentions, etc.) from bulk/marketing emails (daily digests) using either: (1) a priority queue where transactional emails are processed first, (2) separate email sending pipelines/workers for transactional vs. bulk, or (3) rate-limiting the digest batch job so it doesn't monopolize the email sending capacity. Investigate the email service configuration (SendGrid, SES, etc.) for queue depth, throughput limits, and whether a priority tier is available. As an immediate mitigation, consider scheduling the digest job during off-peak hours (e.g., 6am or overnight).

## Proposed Test Case
After implementing the fix, send a batch of 200 digest emails and simultaneously trigger a task assignment notification. Verify the task assignment email arrives within 2 minutes regardless of digest batch status. Run this test during the digest sending window to confirm transactional emails are no longer blocked.

## Information Gaps
- Which email service/provider is used (SendGrid, SES, self-hosted SMTP, etc.) and its throughput limits
- Whether the email pipeline has any existing queue prioritization mechanism
- Exact queue depth and processing rate metrics from the email service during the 9am window
- Whether the digest is sent as 200 individual emails or uses a bulk sending API
