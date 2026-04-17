# Triage Summary

**Title:** Email notification delivery delayed 2-4 hours (team-wide, email-only, worse in mornings)

## Problem
For the past week, email notifications for task assignments have been arriving 2-4 hours late instead of within 1-2 minutes. The delay is team-wide and affects only email — in-app notifications arrive on time. The delay is worst for notifications sent around 9-10am and somewhat better in the afternoon.

## Root Cause Hypothesis
The email sending pipeline has a bottleneck — most likely in the email queue worker or due to rate limiting from the email service provider. The morning spike pattern suggests either overnight queue buildup that takes hours to drain, worker capacity that cannot keep up with morning peak load, or email provider throttling that kicks in during high-volume periods. Since in-app notifications are unaffected, the core notification dispatch is working correctly; the problem is isolated to the email delivery path.

## Reproduction Steps
  1. Assign a task to a user around 9-10am
  2. Note the timestamp of the assignment
  3. Check when the email notification arrives
  4. Compare with the in-app notification timestamp
  5. Repeat in the afternoon to observe the reduced (but still present) delay

## Environment
Team-wide, all users affected. Email and in-app notification channels in use. Issue began approximately one week ago.

## Severity: high

## Impact
Entire team is missing or delayed in seeing task assignments, causing missed deadlines. The delay undermines the core workflow of task assignment and accountability.

## Recommended Fix
1. Check the email queue (e.g., Sidekiq, Celery, SQS) for backlog depth and processing rate — look for jobs piling up during morning hours. 2. Review email service provider dashboard (e.g., SendGrid, SES, Mailgun) for throttling, rate limit changes, or delivery delays. 3. Check deployment and config change history from ~1 week ago for anything affecting the email pipeline (worker count changes, provider plan changes, new rate limits). 4. Examine email worker logs for errors, retries, or slow processing. 5. If queue-based: consider scaling workers during peak hours or switching to a priority queue for time-sensitive notifications.

## Proposed Test Case
Send a task assignment notification and assert that the corresponding email is enqueued within 30 seconds and delivered (or handed off to the email provider) within 2 minutes. Add monitoring/alerting on email queue depth and oldest-job age to catch future regressions.

## Information Gaps
- Exact email service provider and sending infrastructure (queue system, worker setup)
- What changed approximately one week ago (deployment, config, provider plan)
- Whether email delivery metrics or queue dashboards show a visible backlog pattern
