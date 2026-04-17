# Triage Summary

**Title:** Email notifications delayed 2-4 hours, correlated with daily digest generation at ~9am

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours. The delays are worst in the morning (9-10am) and improve by afternoon. This began approximately one week ago and affects all team members, all projects, and all task types. Email headers confirm TaskFlow is sending late, not the email provider delivering late.

## Root Cause Hypothesis
The daily digest feature, introduced ~1 week ago, is likely blocking or starving the email notification queue/worker. The digest generates and sends around 9am, which directly overlaps with the worst notification delays. The most probable mechanism is that digest generation (potentially for all users) monopolizes a shared email sending queue or worker pool, causing assignment notifications to back up until the digest batch completes.

## Reproduction Steps
  1. Ensure the daily digest feature is enabled (it appears to be on by default)
  2. Assign a task to a user at approximately 9:00am (when the digest is being generated/sent)
  3. Observe the delay between task assignment time and the email's sent timestamp
  4. Compare by assigning another task in the afternoon (e.g., 2pm) and measuring the same delay
  5. Check the email/notification worker queue depth and processing times during both windows

## Environment
Affects all team members. Issue began approximately one week ago, coinciding with the rollout of the daily digest email feature. No specific project, task type, or user role is singled out.

## Severity: high

## Impact
Team members are missing task deadlines because they are unaware of assignments for hours. The entire team is affected every morning. No known workaround exists since the digest cannot be easily disabled by end users.

## Recommended Fix
Investigate whether the daily digest job and real-time notification emails share a worker pool or queue. Likely fixes: (1) Move digest generation to a separate queue/worker so it cannot block transactional notifications, (2) Prioritize assignment notifications over batch digest emails, or (3) Schedule digest generation during off-peak hours (e.g., early morning before work starts). Also verify that the digest job is not holding a database lock or connection pool that delays notification queries.

## Proposed Test Case
After fixing, assign a task at 9:00am (during digest generation window) and verify the notification email is sent within 2 minutes. Run this test with digest enabled for a realistic user count to confirm no regression under load.

## Information Gaps
- Exact number of users/teams receiving the daily digest (affects queue load estimation)
- Whether a user with digest disabled would still experience delays (confirms shared-queue hypothesis but requires developer-side testing)
- Architecture details of the email sending pipeline (shared queue vs. separate workers)
