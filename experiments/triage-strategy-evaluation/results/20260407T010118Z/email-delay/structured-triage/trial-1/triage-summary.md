# Triage Summary

**Title:** Email notifications for task assignments delayed 2-4 hours, worst during morning hours

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now consistently delayed by 2-4 hours. The issue has persisted for approximately one week and affects the entire team (at least 3-4 users confirmed). The delay is most severe for notifications sent around 9-10am, with afternoon notifications being faster but still delayed. This is causing missed deadlines because team members don't learn about task assignments in time.

## Root Cause Hypothesis
Most likely a queue backlog or rate-limiting issue in the email sending pipeline. The morning peak pattern (9-10am showing worst delays) suggests the notification queue is overwhelmed during high-activity hours when many tasks are assigned simultaneously, and the queue gradually drains through the afternoon. This could be caused by a recent change in v2.3.x to the email worker (reduced concurrency, new rate limits), a degraded connection to the mail relay, or a growing queue table that slows processing.

## Reproduction Steps
  1. Assign a task to a team member in TaskFlow around 9-10am
  2. Note the exact timestamp of the assignment
  3. Monitor when the email notification is received in Outlook
  4. Observe that the notification arrives 2-4 hours after assignment
  5. Repeat in the afternoon and observe a shorter but still present delay

## Environment
TaskFlow v2.3.1 (approximate), Outlook email provider, multiple users on the same team affected

## Severity: high

## Impact
Entire teams are missing deadlines because task assignment notifications arrive hours late. At least 3-4 users confirmed affected. Directly impairs the core workflow of assigning and acting on tasks.

## Recommended Fix
Investigate the email notification queue/worker: (1) Check queue depth and processing times, especially during 9-10am window. (2) Review email worker logs for errors, retries, or throttling around that timeframe. (3) Check if any recent changes to email sending configuration, worker concurrency, or rate limits were deployed. (4) Verify SMTP relay connection health and response times. (5) Check if the notification jobs table has grown significantly and needs indexing or cleanup.

## Proposed Test Case
Send a task assignment notification at peak hours (9-10am) and verify it is delivered within 2 minutes. Run a load test simulating typical morning assignment volume and confirm all notifications are dispatched within the expected SLA.

## Information Gaps
- Exact TaskFlow version (reporter said 'I believe v2.3.1' but was unsure)
- Server-side email queue logs and metrics (not available from reporter)
- Whether this coincided with a specific deployment or configuration change
- Whether the delay is in TaskFlow's sending or in SMTP relay processing
