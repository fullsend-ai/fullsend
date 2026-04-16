# Conversation: email-delay x omo-prometheus

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the time-of-day pattern is a really useful observation.

To help narrow this down: is this affecting **all users** across your organization, or have you only confirmed it for specific people or teams? And do you know if anything changed roughly a week ago — a deployment, a configuration change, or a migration of any kind (e.g., email provider, queue infrastructure)?

---

### reporter

I've confirmed it with at least 3 other people on my team, and someone from the design team mentioned the same thing, so I think it's affecting everyone. As for what changed — I'm not sure about any technical deployments, but I did notice we started getting these new daily digest emails about a week ago? Like a marketing summary or something. Not sure if that's related. I don't know about any infrastructure changes though, I'm not on the engineering side.

---

### triage-agent

That daily digest email is a really promising lead — the timing lines up perfectly. Do you know roughly what time of day those digest emails arrive? Specifically, do they land around 9am or earlier in the morning? I'm wondering if a large batch of digest emails might be flooding the same email queue that handles your task notifications, which would explain why morning notifications are the slowest.

---

### reporter

Yeah, I think they usually show up around 9am or maybe a little after. That would make sense timing-wise since that's exactly when the delays are worst. I hadn't really connected the two but now that you mention it, the digest emails started showing up right around the same time the notification delays began. Could that really be causing it though? It's just one email per person isn't it?

---

### triage-agent

Great question — and it actually gets right to the heart of what I think is happening. One email per person sounds small, but it depends on scale: roughly how many people are in your organization using TaskFlow? If it's hundreds or thousands of users, then that daily digest is actually a massive burst of emails all hitting the queue at once around 9am. That would push your task notification emails to the back of the line, and the queue would take hours to drain — which matches exactly what you're seeing. So: approximately how many users are on your TaskFlow instance?

---

### reporter

I think we have around 200 people using TaskFlow. So yeah, if every person gets a digest email at 9am, that's 200 emails all at once... but that still doesn't seem like it should cause hours of delay? Or maybe I'm not understanding how the email system works. Either way, the delays are definitely real and it's been a problem for the whole team.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours since daily digest feature launch — likely email queue saturation","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now arriving 2-4 hours late. The delays are worst for notifications sent around 9-10am and improve in the afternoon. This has been occurring for approximately one week and affects the entire organization (~200 users across multiple teams). Users are missing task deadlines because they don't learn about assignments in time.","root_cause_hypothesis":"The daily digest email feature, launched approximately one week ago, sends ~200 emails simultaneously around 9am. This batch saturates the email sending queue, pushing task notification emails to the back. The queue then takes hours to drain, causing the observed delays. The time-of-day pattern (worst at 9am, improving through the afternoon as the queue clears) strongly supports this. The fact that 200 emails cause multi-hour delays suggests the email queue has low throughput — possibly due to rate limiting by the email provider, serialized sending, or a single-worker queue configuration.","reproduction_steps":["Assign a task to a user around 9:00-9:30am (after daily digests are sent)","Observe that the notification email arrives 2-4 hours late","Assign a task in the late afternoon when the queue has drained","Observe that the notification arrives faster (but still slower than the pre-digest baseline of 1-2 minutes)"],"environment":"Organization-wide, ~200 TaskFlow users, issue began approximately one week ago coinciding with the launch of a daily digest email feature. No other known infrastructure changes.","severity":"high","impact":"All ~200 users are affected. Users are missing task deadlines because assignment notifications arrive hours late. Worst impact is in the morning when most task assignments likely occur.","recommended_fix":"1. **Immediate investigation:** Check the email queue depth and throughput around 9am — confirm the digest batch is the bottleneck. 2. **Quick mitigation:** Separate the digest emails onto a different queue or sending channel so they don't block transactional notifications (task assignments, deadline reminders). 3. **If a single queue must be used:** Prioritize transactional notifications over batch/marketing emails. 4. **Longer term:** Stagger digest email sending over a wider window (e.g., 8-10am) rather than all at once, and investigate why ~200 emails cause multi-hour delays — the queue throughput may be misconfigured or the email provider rate limit may be very low.","proposed_test_case":"After separating queues (or implementing priority), assign a task at 9:05am (peak digest time) and verify the notification email arrives within 2 minutes. Repeat across multiple days to confirm consistency. Also verify digest emails themselves still arrive within a reasonable window.","information_gaps":["Email queue architecture (single queue vs. multiple, worker count, throughput limits)","Email provider and any rate limiting in effect","Whether the digest feature was intentionally added to the same queue or if it was an oversight","Exact send rate — whether 200 emails are sent simultaneously or serialized","Whether any email delivery errors or retries are compounding the delay"]}

---
