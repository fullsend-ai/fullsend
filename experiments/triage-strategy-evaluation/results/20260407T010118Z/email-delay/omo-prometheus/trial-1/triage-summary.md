# Triage Summary

**Title:** Task notification emails delayed 2-4 hours due to email queue congestion from daily digest feature

## Problem
Since the daily digest/newsletter feature was introduced approximately one week ago, task assignment notification emails are arriving 2-4 hours late. Delays are worst in the morning (9-10am) and improve in the afternoon. All users on the reporter's team are affected, causing missed deadlines.

## Root Cause Hypothesis
The daily digest feature sends a bulk batch of emails to all users (~hundreds) at 9am each morning. These digest emails flood the email sending queue, and task assignment notifications — which are time-sensitive — get stuck behind the bulk batch. The queue drains over 2-4 hours, after which afternoon notifications flow more normally.

## Reproduction Steps
  1. Have the daily digest feature enabled (default for the org)
  2. Wait for 9am when the digest batch fires
  3. Assign a task to a user shortly after 9am
  4. Observe that the notification email arrives 2-4 hours later
  5. Compare with a task assigned in the afternoon — notification arrives faster

## Environment
TaskFlow instance with ~200 users, all receiving the daily digest. No recent changes to email provider or team size. Issue began approximately one week ago, coinciding with the digest feature launch.

## Severity: high

## Impact
All users in the organization experience delayed task assignment notifications, causing missed deadlines. Users are forced to manually check the TaskFlow dashboard as a workaround, undermining the purpose of email notifications.

## Recommended Fix
Investigate the email sending queue architecture. Likely fixes in order of preference: (1) Implement priority lanes in the email queue so transactional emails (task assignments, mentions) are sent ahead of bulk emails (digests, newsletters). (2) Rate-limit or stagger the digest send over a longer window rather than enqueuing all at once. (3) Move digest emails to a separate queue or sending infrastructure so they don't compete with transactional notifications.

## Proposed Test Case
Send a batch of N digest emails (simulating the 9am burst) and immediately enqueue a task assignment notification. Verify the task notification is delivered within the SLA (e.g., under 2 minutes) regardless of digest queue depth. Test with N matching production scale (~200+).

## Information Gaps
- Exact email queue implementation (single queue vs. multiple, queue technology used)
- Whether the digest feature has any batching or rate-limiting configuration already
- Precise number of digest recipients and emails per digest run
- Server-side email sending logs confirming the queue backup theory
