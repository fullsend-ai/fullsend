# Triage Summary

**Title:** Email notifications delayed 2-4 hours since digest feature rollout — likely shared email pipeline saturation

## Problem
Transactional email notifications (e.g., task assignments) are arriving 2-4 hours late instead of within 1-2 minutes. The delays are worst in the morning (9-10am) and somewhat better in the afternoon. In-app notifications remain instant. The issue is system-wide, affecting multiple users across teams, and has been occurring for approximately one week.

## Root Cause Hypothesis
The recently launched daily digest/newsletter feature is likely sharing the same email sending pipeline (queue, provider, or rate-limited API) as transactional notifications. Digest emails are generated and enqueued in the morning, creating a large burst that saturates send capacity or hits provider rate limits, pushing transactional emails to the back of the queue. This explains both the morning severity pattern and the one-week timeline aligning with the digest feature rollout.

## Reproduction Steps
  1. Assign a task to a user in TaskFlow around 9-10am (when delays are worst)
  2. Observe that the in-app bell notification appears within seconds
  3. Observe that the corresponding email notification arrives 2-4 hours later
  4. Repeat in the afternoon and note the email delay is shorter but still present

## Environment
System-wide across all users. No specific OS/browser dependency since the issue is server-side email delivery. Timing: started approximately one week ago, coinciding with the rollout of a daily digest/newsletter email feature.

## Severity: high

## Impact
All users receiving email notifications are affected. Team members are missing task assignment deadlines because they rely on email notifications and don't see assignments for hours. Multiple teams have independently noticed and complained about the issue.

## Recommended Fix
1. Investigate whether the digest feature shares a send queue or email provider rate limit with transactional emails. 2. If so, separate transactional emails into a high-priority queue/channel distinct from bulk digest sends. 3. Check the email provider's rate limit dashboard for throttling events correlating with digest send times. 4. As an immediate mitigation, consider shifting digest sends to off-peak hours (e.g., early morning before business hours) or throttling digest throughput to leave headroom for transactional emails.

## Proposed Test Case
After the fix, send a batch of digest emails and simultaneously trigger a task assignment notification. Verify the transactional email arrives within 2 minutes regardless of digest volume. Run this test during the morning window (9-10am) to confirm the peak-hour delay is resolved.

## Information Gaps
- Exact email provider and sending architecture (shared queue vs. separate channels) — requires codebase/infra investigation
- Whether the provider is returning rate-limit or throttle responses in logs during morning hours
- Exact volume of digest emails being sent and whether it exceeds provider tier limits
