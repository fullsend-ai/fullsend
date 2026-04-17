# Triage Summary

**Title:** Task assignment email notifications delayed 2-4 hours due to queue contention with daily digest batch job

## Problem
Task assignment notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours. The delays began approximately one week ago, coinciding with the rollout of a new daily digest/summary email feature. Delays are worst in the morning (9-10am) when the digest batch fires, and improve in the afternoon as the queue drains.

## Root Cause Hypothesis
The daily digest batch job sends a large volume of emails through the same queue as transactional task-assignment notifications. When the digest batch fires in the morning, it floods the queue, and transactional notifications are processed FIFO behind the digest emails rather than being prioritized. This creates a multi-hour backlog that clears by afternoon.

## Reproduction Steps
  1. Wait for the daily digest batch to fire (likely around 8-9am)
  2. Assign a task to a user shortly after the digest batch starts
  3. Observe that the assignment notification email is delayed by 2-4 hours
  4. Assign a task in the afternoon (after digest backlog clears) and observe faster delivery

## Environment
Production environment. Affects all users receiving task assignment notifications. Timing correlates with daily digest batch schedule.

## Severity: high

## Impact
All users receiving task assignment notifications are affected. People are missing deadlines because they don't learn about new assignments for hours. Morning assignments are most impacted.

## Recommended Fix
Separate transactional emails (task assignments, deadline reminders) from bulk/batch emails (daily digests) so they don't compete for the same queue. Options include: (1) Use a dedicated high-priority queue for transactional notifications, (2) Use a separate email sending pipeline for batch digests, (3) Rate-limit the digest batch job so it doesn't saturate the shared queue, (4) Send digests via a different email service or provider entirely. Option 1 or 2 is preferred as it structurally prevents recurrence.

## Proposed Test Case
Enqueue a batch of N digest emails (matching production volume), then immediately enqueue a transactional notification. Verify the transactional notification is delivered within the expected SLA (e.g., under 2 minutes) regardless of digest queue depth.

## Information Gaps
- Exact queue implementation (Redis, SQS, database-backed, etc.) — developer can determine from codebase
- Number of digest emails sent per batch — developer can check from logs or batch job config
- Whether the email provider has sending rate limits contributing to the backlog
