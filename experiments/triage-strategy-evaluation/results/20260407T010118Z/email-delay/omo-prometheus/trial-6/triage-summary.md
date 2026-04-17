# Triage Summary

**Title:** Email notifications delayed 2-4 hours due to queue saturation from new daily digest feature

## Problem
Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is worst in the morning (9-10am) and less severe in the afternoon. In-app notifications are unaffected, confirming the issue is in the email delivery pipeline, not notification generation.

## Root Cause Hypothesis
The recently introduced daily digest email feature (rolled out ~1 week ago) sends bulk emails to all company users around 9am. These digest emails are likely sharing the same email sending queue as transactional task-assignment notifications. The high volume of digest emails saturates the queue each morning, pushing time-sensitive assignment notifications to the back of the line. As the digest backlog clears through the day, afternoon notifications experience shorter delays.

## Reproduction Steps
  1. Wait for daily digest emails to begin sending (around 9am)
  2. Assign a task to a user in TaskFlow shortly after 9am
  3. Observe that the in-app notification appears immediately
  4. Observe that the email notification arrives 2-4 hours later
  5. Repeat the same assignment in the afternoon and observe a shorter delay

## Environment
All users on the same company email domain. TaskFlow instance with the daily digest feature enabled (deployed approximately one week ago). Specific email sending infrastructure unknown — IT admin has visibility.

## Severity: high

## Impact
All users company-wide are affected. Team members are missing task assignment deadlines because they rely on email notifications and don't see assignments for hours. Direct productivity and deadline impact.

## Recommended Fix
1. Separate the email sending queues: transactional notifications (task assignments, mentions, etc.) should use a higher-priority queue than bulk/marketing emails (daily digests). 2. As an immediate mitigation, consider rate-limiting or staggering the digest send (e.g., spread over 30-60 minutes rather than bursting all at once). 3. Alternatively, move digest emails to a dedicated sending worker/queue so they never compete with transactional email. 4. Review the email provider's rate limits — the burst of digests may also be triggering provider-side throttling.

## Proposed Test Case
Send a task assignment notification while a batch of digest emails is being queued. Verify the assignment notification is delivered within 2 minutes regardless of digest queue depth. Additionally, verify that moving digest emails to a separate queue or priority lane eliminates the morning delay pattern.

## Information Gaps
- Exact email sending infrastructure (SMTP server, third-party service like SendGrid/SES, queue implementation)
- Exact number of digest recipients (company size) to quantify queue load
- Whether the email provider is applying rate limiting or throttling during the digest burst
- Whether digest and transactional emails already use separate queues or share one
