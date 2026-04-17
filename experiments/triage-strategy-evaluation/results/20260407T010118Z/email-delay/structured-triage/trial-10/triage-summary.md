# Triage Summary

**Title:** Email notifications delayed 2-4 hours since v2.3.1 update (possible daily digest queue interference)

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is system-wide across all projects and task types, worse for morning assignments (9-10am), and started approximately one week ago coinciding with a TaskFlow update.

## Root Cause Hypothesis
The recently introduced daily digest feature (released ~1 week ago with or near v2.3.1) likely changed the email sending pipeline — possibly routing individual notifications through the same queue or batching mechanism used for digests, or introducing a shared worker pool that prioritizes digest compilation during morning hours, causing immediate notifications to be delayed.

## Reproduction Steps
  1. Assign a task to a user via the standard TaskFlow UI task creation form
  2. Note the assignment timestamp shown in TaskFlow
  3. Wait for the assignee to receive the email notification
  4. Compare the TaskFlow assignment timestamp to the email send timestamp in the email headers
  5. Expect to see a 2-4 hour gap, especially for assignments made in the 9-10am window

## Environment
TaskFlow v2.3.1, email delivered via organization SMTP relay (specific configuration managed by IT), cloud or self-hosted not confirmed but likely self-hosted given SMTP relay setup

## Severity: high

## Impact
All users across all projects are affected. Delayed notifications cause team members to miss task assignments for hours, leading to missed deadlines and workflow disruption. Morning assignments are most severely impacted.

## Recommended Fix
Investigate the email sending pipeline changes introduced alongside the daily digest feature. Specifically: (1) Check whether immediate notification jobs and digest jobs share a queue or worker pool; (2) Look for batching or scheduling logic that may be inadvertently capturing individual notifications; (3) Examine queue depth and processing times, especially during the morning window when digest compilation likely runs; (4) Review the job scheduling configuration for the digest feature to see if it blocks or deprioritizes other email jobs.

## Proposed Test Case
Assign a task via the UI and verify that the email notification is sent (check email headers for send timestamp) within 2 minutes of assignment. Run this test both during morning hours (9-10am) and afternoon to confirm consistent delivery times. Additionally, test with the daily digest feature disabled to confirm it as the root cause.

## Information Gaps
- No server-side logs or email queue metrics to confirm where the delay occurs (TaskFlow job queue vs. SMTP relay)
- Exact SMTP relay configuration and whether IT made any changes on that side
- Whether the daily digest feature is enabled for the affected users/organization
- Exact TaskFlow release that was deployed ~1 week ago (reporter believes v2.3.1 but is not certain of timing)
