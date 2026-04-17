# Triage Summary

**Title:** Email notifications delayed 2-4 hours since daily digest feature launch — likely email queue saturation

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now arriving 2-4 hours late. The delays are worst for notifications sent around 9-10am and improve in the afternoon. This has been occurring for approximately one week and affects the entire organization (~200 users across multiple teams). Users are missing task deadlines because they don't learn about assignments in time.

## Root Cause Hypothesis
The daily digest email feature, launched approximately one week ago, sends ~200 emails simultaneously around 9am. This batch saturates the email sending queue, pushing task notification emails to the back. The queue then takes hours to drain, causing the observed delays. The time-of-day pattern (worst at 9am, improving through the afternoon as the queue clears) strongly supports this. The fact that 200 emails cause multi-hour delays suggests the email queue has low throughput — possibly due to rate limiting by the email provider, serialized sending, or a single-worker queue configuration.

## Reproduction Steps
  1. Assign a task to a user around 9:00-9:30am (after daily digests are sent)
  2. Observe that the notification email arrives 2-4 hours late
  3. Assign a task in the late afternoon when the queue has drained
  4. Observe that the notification arrives faster (but still slower than the pre-digest baseline of 1-2 minutes)

## Environment
Organization-wide, ~200 TaskFlow users, issue began approximately one week ago coinciding with the launch of a daily digest email feature. No other known infrastructure changes.

## Severity: high

## Impact
All ~200 users are affected. Users are missing task deadlines because assignment notifications arrive hours late. Worst impact is in the morning when most task assignments likely occur.

## Recommended Fix
1. **Immediate investigation:** Check the email queue depth and throughput around 9am — confirm the digest batch is the bottleneck. 2. **Quick mitigation:** Separate the digest emails onto a different queue or sending channel so they don't block transactional notifications (task assignments, deadline reminders). 3. **If a single queue must be used:** Prioritize transactional notifications over batch/marketing emails. 4. **Longer term:** Stagger digest email sending over a wider window (e.g., 8-10am) rather than all at once, and investigate why ~200 emails cause multi-hour delays — the queue throughput may be misconfigured or the email provider rate limit may be very low.

## Proposed Test Case
After separating queues (or implementing priority), assign a task at 9:05am (peak digest time) and verify the notification email arrives within 2 minutes. Repeat across multiple days to confirm consistency. Also verify digest emails themselves still arrive within a reasonable window.

## Information Gaps
- Email queue architecture (single queue vs. multiple, worker count, throughput limits)
- Email provider and any rate limiting in effect
- Whether the digest feature was intentionally added to the same queue or if it was an oversight
- Exact send rate — whether 200 emails are sent simultaneously or serialized
- Whether any email delivery errors or retries are compounding the delay
