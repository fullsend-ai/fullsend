# Triage Summary

**Title:** Daily digest email job blocks real-time notification delivery, causing 2-4 hour delays

## Problem
Since the daily digest feature was launched approximately one week ago, task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and affect all users across multiple teams. Afternoon notifications are also delayed but less severely.

## Root Cause Hypothesis
The daily digest email job, which runs around 9am, is saturating or blocking the shared email sending queue. Real-time task assignment notifications are queued behind a large batch of digest emails, causing them to wait until the digest batch completes. The afternoon improvement suggests the queue gradually drains, but never fully catches up during peak hours.

## Reproduction Steps
  1. Assign a task to a user in the morning around 9-10am when daily digests are being sent
  2. Observe that the notification email arrives 2-4 hours late instead of within 1-2 minutes
  3. Repeat the same assignment in the late afternoon and observe a shorter (but still present) delay

## Environment
Production environment, all users affected, started approximately one week ago coinciding with daily digest feature rollout

## Severity: high

## Impact
All users across multiple teams are missing task assignment deadlines because notifications arrive hours late. This directly affects team productivity and deadline adherence.

## Recommended Fix
Investigate whether real-time notifications and digest emails share the same email sending queue or worker pool. Likely fixes in order of preference: (1) Use a separate high-priority queue/worker for real-time notifications so they bypass digest batch processing, (2) Rate-limit or throttle the digest job so it doesn't monopolize the email pipeline, (3) Move digest sending to an off-peak window (e.g., 6am or overnight) as a quick mitigation.

## Proposed Test Case
Send a batch of digest emails (simulating the morning digest job) and simultaneously trigger a real-time task assignment notification. Verify the real-time notification is delivered within the expected SLA (< 2 minutes) regardless of digest queue depth.

## Information Gaps
- Exact email queue architecture (shared queue vs. separate queues) — answerable from code inspection
- Volume of digest emails sent per morning run — available from production metrics
- Whether the email provider has rate limits that the digest job is hitting — check provider docs and logs
