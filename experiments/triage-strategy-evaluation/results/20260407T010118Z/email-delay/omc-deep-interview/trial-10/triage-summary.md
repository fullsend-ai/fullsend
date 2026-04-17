# Triage Summary

**Title:** Email notifications delayed 2-4 hours, worst during morning digest send window

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delays are worst in the morning (9-10am) and improve in the afternoon. Delayed emails trickle in gradually rather than arriving in a burst, suggesting a throughput bottleneck rather than a queue-and-release issue. In-app notifications are unaffected. The problem affects all users, not specific accounts.

## Root Cause Hypothesis
A daily digest/summary email feature was rolled out approximately one week ago — the same time delays began. The digest likely sends around 9am to some or all of the ~200 active users, saturating the email sending pipeline (e.g., SMTP connection pool, rate-limited email provider quota, or a shared send queue). Transactional notification emails are stuck behind or competing with the digest batch, causing them to trickle out slowly as the pipeline drains. By afternoon, the backlog has cleared and latency returns to near-normal.

## Reproduction Steps
  1. Assign a task to a user in the afternoon and measure email delivery latency (baseline: ~5-10 minutes currently)
  2. Assign a task to a user at ~9:00am the next morning, shortly before or during the digest send window, and measure email delivery latency (expected: 2-4 hours)
  3. Compare the two measurements to confirm the morning correlation
  4. Check email provider logs/dashboard for send volume spikes and throttling around 9am

## Environment
TaskFlow instance with ~200 users, team of ~12 affected reporters. Daily digest feature recently deployed. Exact email provider and queue infrastructure unknown but should be checked.

## Severity: high

## Impact
All TaskFlow users are affected. Team members are missing task assignment deadlines because they don't learn about assignments for hours. Workaround: check the TaskFlow dashboard directly for new assignments rather than relying on email.

## Recommended Fix
1. Investigate the email sending pipeline — check if digest emails and transactional notification emails share the same queue or rate limit. 2. If they share a queue, separate them: give transactional emails priority or use a dedicated sending path. 3. If the email provider imposes rate limits, check whether the digest batch is consuming the quota; consider sending digests via a separate provider, throttling digest sends over a wider window, or upgrading the rate limit. 4. As an immediate mitigation, consider disabling or staggering the digest feature to confirm it resolves the latency.

## Proposed Test Case
After the fix, assign a task at 9:00am during the digest send window and verify the notification email arrives within 2 minutes. Run this test on multiple consecutive mornings to confirm consistent low latency. Also verify digest emails still send successfully.

## Information Gaps
- Exact email infrastructure details (provider, queue system, rate limits)
- Whether the digest is sent to all 200 users or only a subset
- Concrete morning test measurement from the reporter (in progress — reporter agreed to test tomorrow)
- Server-side email queue metrics and logs from the past week
