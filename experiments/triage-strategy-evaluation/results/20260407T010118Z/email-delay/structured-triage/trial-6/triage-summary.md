# Triage Summary

**Title:** Email notifications delayed 2-4 hours (regression in sending pipeline, ~1 week)

## Problem
All email notifications (task assignments, due date reminders, etc.) are being sent with significant delays. Morning notifications (9-10am) are delayed 2-4 hours; afternoon notifications are delayed 30-60 minutes. This started approximately one week ago; prior to that, notifications arrived within a minute.

## Root Cause Hypothesis
The time-of-day pattern (worse in morning, improving through afternoon) strongly suggests a queued/batched email sending pipeline that is either backed up, throttled, or whose background worker is under-provisioned. A likely cause is a change ~1 week ago to the email job queue configuration, a degraded background worker, or a new rate limit being hit — the morning backlog accumulates overnight or during peak assignment hours and gradually drains through the day.

## Reproduction Steps
  1. Log in as a user with admin/manager role on TaskFlow v2.3.1
  2. Open any task's detail page
  3. Select a user from the assignee dropdown and save
  4. Observe the time the assignment is made vs. when the notification email is actually sent (check email headers for the sending timestamp)
  5. Repeat in the morning (9-10am) for maximum delay reproduction

## Environment
TaskFlow v2.3.1, cloud-hosted instance (to be confirmed with IT), recipients on Outlook/Exchange corporate mail

## Severity: high

## Impact
All users receiving email notifications are affected. Team members are missing task assignment deadlines because they learn of assignments hours late. This disrupts workflow for managers and assignees organization-wide.

## Recommended Fix
Investigate the email sending pipeline on the server side: (1) Check the email job queue (e.g., Sidekiq, Celery, or similar) for backlog depth and processing rate, especially during morning hours. (2) Review any changes deployed ~1 week ago that may have altered queue concurrency, rate limits, or worker count. (3) Check for resource contention on the worker process (CPU, memory, database connections). (4) Verify SMTP send rate limits with the outbound mail provider — a new throttle could explain the draining-backlog pattern.

## Proposed Test Case
Trigger a task assignment notification and assert that the email is enqueued and sent within 60 seconds. Include a time-based integration test that assigns tasks during simulated peak load and verifies send latency stays under the SLA threshold.

## Information Gaps
- Exact hosting details (cloud vs. self-hosted) — reporter was unsure, IT team would confirm
- Server-side email queue logs and metrics (not available to the reporter)
- Whether any deployment or configuration change coincided with the onset ~1 week ago
