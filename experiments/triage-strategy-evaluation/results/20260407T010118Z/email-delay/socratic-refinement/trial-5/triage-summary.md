# Triage Summary

**Title:** Email notifications delayed 2-4 hours, likely due to bulk digest emails saturating shared send queue

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours. The delays are worst in the morning (9-10am) and less severe in the afternoon. In-app notifications are unaffected — only email delivery is delayed.

## Root Cause Hypothesis
A recently-introduced daily digest email feature sends bulk emails to the entire organization (~200 users) at approximately 9am. These bulk sends likely share the same email queue or sending pipeline as transactional notifications (task assignments). The burst of ~200 digest emails saturates the queue or hits rate limits on the email provider, causing transactional emails to queue behind them. As the digest backlog clears through the morning, transactional email latency improves, explaining the afternoon recovery pattern.

## Reproduction Steps
  1. Assign a task to a user at approximately 9:00-9:15am (shortly after daily digests are dispatched)
  2. Note the time the in-app notification appears (expected: immediate)
  3. Note the time the email notification arrives (expected: 2-4 hour delay)
  4. Repeat the same assignment around 3-4pm and compare email delivery latency (expected: shorter delay)
  5. Check the email sending queue depth or logs at 9am vs afternoon to confirm backlog correlation

## Environment
Organization with ~200 users. Daily digest email feature recently enabled. Exact email service provider and queue infrastructure unknown — developer should check.

## Severity: high

## Impact
All users in the organization are affected. Delayed assignment notifications cause people to miss task deadlines because they don't learn about assignments for hours. Morning assignments are most impacted, which is typically when most work is distributed.

## Recommended Fix
1. Investigate whether digest emails and transactional notifications share an email queue or sending pipeline. 2. If shared, separate them: use a dedicated high-priority queue/channel for transactional emails (assignment notifications) and a lower-priority queue for bulk sends (digests). 3. If using an external email provider, check whether the digest burst is hitting rate limits that then throttle all subsequent sends. 4. Consider spreading the digest send over a longer window (e.g., stagger over 30 minutes) rather than sending all ~200 at once. 5. As an immediate mitigation, consider shifting the digest send time to off-peak hours (e.g., 7am or 11pm).

## Proposed Test Case
Send a batch of 200 digest-equivalent emails followed immediately by a transactional notification email. Verify the transactional email arrives within the SLA (e.g., under 2 minutes) regardless of the bulk send. After the fix, repeat at 9am in production and confirm assignment notifications arrive within 1-2 minutes.

## Information Gaps
- Exact email service provider and queue architecture (developer can determine from codebase/infrastructure)
- Whether other users in the organization experience the same delays or just the reporter (very likely systemic given the hypothesis, but not explicitly confirmed)
- Exact date the digest feature was enabled vs. exact date delays began
