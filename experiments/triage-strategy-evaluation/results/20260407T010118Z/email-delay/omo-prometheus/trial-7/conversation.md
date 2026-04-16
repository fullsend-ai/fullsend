# Conversation: email-delay x omo-prometheus

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you noticed (worse in the morning, better in the afternoon) is a really useful data point.

To start narrowing this down: is this affecting all users on your team, or have you only confirmed the delay from your own inbox? And do you know if anything changed about a week ago when this started — a new deployment, a change in team size, a migration, or anything along those lines?

---

### reporter

Yeah, it's affecting everyone on my team — I've heard complaints from at least 3 or 4 other people about late notifications this week. As for what changed, I'm not 100% sure but I think there was some new email feature that rolled out recently? We started getting these daily digest or summary emails around the same time the delays started. Not sure if that's related or just a coincidence, but the timing lines up.

---

### triage-agent

That timing correlation with the daily digest feature is a really important clue — thanks for flagging it.

Can you tell me: what time do those daily digest emails typically arrive in your inbox? And when the individual task-assignment notifications do finally show up late, do they tend to arrive in a burst (several at once) or do they trickle in one at a time? That distinction will help us figure out whether something is batching or queuing them.

---

### reporter

The digest emails usually show up around 9am, I think. Maybe a little after. As for the late notifications — they kind of trickle in over the course of a few hours. Like I'll get one, then another 20 minutes later, then another. It's not like they all show up at once in a big batch. By early afternoon things seem to mostly catch up and notifications come through faster.

---

### triage-agent

Thanks — the trickle pattern is very telling. Here's what I'm thinking is happening, and I'd like your take:

The new daily digest feature likely kicks off around 9am and generates a large batch of digest emails for every user. Those digest emails and your individual task-assignment notifications probably share the same email sending queue. So when the digest job floods the queue each morning, the individual notifications get stuck behind hundreds (or thousands) of digest emails and have to wait their turn — which is why they trickle in slowly over the next few hours rather than arriving all at once.

Does that match what you're seeing? And roughly how many people are on the TaskFlow instance — are we talking dozens of users getting digests, or hundreds/thousands?

---

### reporter

Yeah, that explanation makes a lot of sense actually — it lines up with what we're seeing. The delays started right when the digest emails started, and the morning timing fits perfectly. We have around 200 users on our TaskFlow instance, so if everyone's getting a digest email that could be a lot of emails hitting the queue at once. I think that's probably what's clogging things up.

---

### triage-agent

[RESOLVED] {"title":"Email notification delays caused by shared queue contention with daily digest feature","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours. The delays are worst in the morning (9-10am) and gradually improve through the afternoon. This is causing users to miss task deadlines because they don't learn about assignments in time. All ~200 users on the instance are affected.","root_cause_hypothesis":"The recently launched daily digest email feature generates ~200 digest emails around 9am each morning. These digest emails share the same sending queue as individual task-assignment notifications. The digest job floods the queue, and individual notifications must wait behind the digest backlog. Because the queue processes sequentially (trickle pattern, not burst), notifications are delayed for hours until the queue drains. By early afternoon the backlog clears and notification latency returns to near-normal.","reproduction_steps":["Ensure the daily digest feature is enabled (default for all users)","Assign a task to a user at approximately 9:00-9:30am, shortly after the digest job runs","Observe that the assignment notification email arrives 2-4 hours late","Repeat the assignment at approximately 2:00-3:00pm and observe significantly shorter delay","Optionally: disable the digest feature or reduce digest recipients, then repeat the 9am test to confirm latency returns to normal"],"environment":"TaskFlow instance with ~200 users, daily digest feature recently enabled. Specific email provider/queue infrastructure unknown but the shared queue architecture is the key factor.","severity":"high","impact":"All ~200 users experience delayed task assignment notifications, primarily in the morning. Users are missing deadlines because they don't learn about assignments for hours. The delay undermines the core workflow of timely task assignment.","recommended_fix":"Investigate the email sending architecture. The most likely fix is to separate the digest emails and transactional notifications into distinct queues or priority lanes, so that the bulk digest job does not block time-sensitive individual notifications. Alternatives include: (1) adding a priority field to the queue and prioritizing transactional notifications over digests, (2) rate-limiting or staggering the digest job so it doesn't flood the queue all at once, (3) moving digest generation to an off-peak time (e.g., 6am or overnight). The developer should also check whether the email queue has concurrency or throughput limits that could be raised.","proposed_test_case":"With the digest feature active and ~200 users enrolled, assign a task at 9:05am (just after the digest job fires) and assert that the notification email is delivered within 5 minutes. This should pass after the fix and would have failed before it.","information_gaps":["Exact email queue implementation and provider (in-app queue, SQS, Redis, third-party service, etc.)","Whether the digest job has any built-in rate limiting or concurrency controls","Server-side queue metrics (depth, processing rate) to confirm the hypothesis quantitatively"]}

---
