# Triage Summary

**Title:** Email notifications delayed 2-4 hours, likely caused by new daily digest feature saturating email queue

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours during morning hours (9-10am) and 20-30 minutes in the afternoon. This began approximately one week ago and affects all team members regardless of email provider.

## Root Cause Hypothesis
A new daily digest/marketing summary email feature was rolled out approximately one week ago and sends around 9am. It shares the same sender address (and likely the same email sending infrastructure) as transactional task notifications. The digest batch is probably saturating the email provider's rate limits or flooding a shared sending queue, causing transactional notifications to back up behind bulk marketing emails. Morning delays are worst because the digest send coincides with peak task assignment activity. Afternoon notifications are faster because the digest backlog has cleared.

## Reproduction Steps
  1. Assign a task to a user at approximately 9:15am (during/after daily digest send window)
  2. Observe that the email notification arrives 2-4 hours late
  3. Assign another task to the same user at approximately 2-3pm
  4. Observe that this notification arrives within 20-30 minutes
  5. Check the email sending queue/logs to confirm digest emails are queued ahead of transactional notifications

## Environment
Affects all team members across multiple email providers (Outlook and Gmail confirmed). Issue began approximately one week ago, coinciding with the rollout of a new daily digest/marketing email feature.

## Severity: high

## Impact
Team-wide. Multiple team members are missing task assignment deadlines because notifications arrive hours after assignment. No workaround has been identified. Directly impairs the core task management workflow.

## Recommended Fix
1. Investigate the email sending infrastructure — check whether transactional notifications and the daily digest share a queue or rate-limited sender. 2. Separate transactional emails (task notifications) from bulk emails (daily digest) into distinct sending queues or channels with independent rate limits. 3. Prioritize transactional emails over bulk/marketing sends. 4. Consider using a dedicated sender address or subdomain for the digest to avoid shared rate limits. 5. As an immediate mitigation, consider shifting the digest send to an off-peak window (e.g., early morning before work hours) to avoid competing with transactional notifications.

## Proposed Test Case
After separating email queues: send a daily digest to N users and simultaneously trigger a task assignment notification. Verify the task notification arrives within 2 minutes regardless of digest volume. Repeat at 9am to confirm the morning delay is resolved.

## Information Gaps
- Exact email sending infrastructure (shared queue, third-party provider, rate limits)
- Precise volume of digest emails sent in the morning batch
- Whether the digest feature has a configuration for send time or batching
- Whether in-app notifications (if they exist) are also delayed or only email
