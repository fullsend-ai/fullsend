# Triage Summary

**Title:** Email notifications delayed 2-4 hours, worst during morning hours (since ~1 week ago)

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is most severe for notifications sent around 9-10am and somewhat better in the afternoon. The issue affects the entire team, not a single user, and began approximately one week ago with no client-side changes.

## Root Cause Hypothesis
The morning-heavy delay pattern suggests a queue backlog or rate-limiting issue on the sending side. A likely cause is that a background job queue (e.g., email worker, cron-based batch sender) is either under-provisioned, stuck processing a backlog that accumulates overnight, or hitting a send-rate limit that throttles morning burst traffic. A recent change roughly one week ago — such as a deployment, config change, or increased user/task volume — likely triggered this.

## Reproduction Steps
  1. Assign a task to a user in TaskFlow around 9-10am
  2. Note the timestamp of the assignment
  3. Observe when the email notification arrives in the assignee's inbox
  4. Compare: expected delivery within 1-2 minutes, actual delivery 2-4 hours later
  5. Repeat in the afternoon to confirm shorter (but still abnormal) delays

## Environment
TaskFlow v2.3.1, Outlook via corporate mail server, entire team affected

## Severity: high

## Impact
Team-wide. Users are missing task deadlines because assignment notifications arrive hours late, undermining the core workflow TaskFlow is meant to support.

## Recommended Fix
Investigate the email sending pipeline: (1) Check the mail job queue for backlog depth, especially at morning peak. (2) Review any deployments or config changes from ~1 week ago. (3) Check for rate-limiting or throttling on the SMTP relay or mail provider. (4) Examine email worker logs for errors, retries, or slow processing. (5) Check if the mail server or queue infrastructure was scaled down or if task/user volume increased.

## Proposed Test Case
Assign a task and assert that the corresponding email notification is enqueued within 30 seconds and delivered (SMTP handoff confirmed) within 2 minutes, under both low-load and simulated morning-peak-load conditions.

## Information Gaps
- Server-side email queue metrics and logs from the past week
- Whether any deployment or infrastructure change coincided with the onset
- Whether non-email notifications (in-app, mobile push) are also delayed
