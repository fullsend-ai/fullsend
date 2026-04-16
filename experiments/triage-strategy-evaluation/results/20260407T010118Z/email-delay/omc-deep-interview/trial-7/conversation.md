# Conversation: email-delay x omc-deep-interview

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you noticed (worse in the morning, better in the afternoon) is a really useful clue.

Did anything change about a week ago when this started? For example, a deployment, a configuration change, a migration, or a change in email provider or notification settings? Even something that seemed unrelated could help us narrow this down.

---

### reporter

Hmm, I'm not 100% sure about deployments or anything like that — I don't have visibility into that side of things. But I did notice that around the same time, we started getting these new daily digest emails from TaskFlow. Like a marketing summary or something? They show up in my inbox every morning. I'm not sure if that's related, but the timing lines up with when the delays started. Other than that, nothing else changed on our end that I'm aware of.

---

### triage-agent

That's a really helpful observation about the daily digest emails — the timing overlap is suspicious. A couple of quick follow-ups on that: roughly what time do those digest emails land in your inbox each morning? And have you noticed whether the task notification delays seem to improve after the digest email arrives, or are they unrelated in timing? Basically trying to understand if the delays and the digests might be competing for the same pipeline.

---

### reporter

The digest emails usually show up around 9am, give or take. Now that you mention it, yeah, the delays do seem to get better as the morning goes on. Like if someone assigns me a task at 9:15am it might take 3-4 hours to get the notification, but by early afternoon things are more like 30 minutes to an hour. I hadn't really connected the two but I guess they could be related if they're going through the same email system? We have about 200 people on the team and I think everyone gets those digest emails.

---

### triage-agent

That's a really strong lead — 200 digest emails hitting at 9am could definitely explain the morning bottleneck.

One thing I want to make sure we're not assuming: is it specifically *task assignment* notifications that are delayed, or have you noticed delays with other notification types too (like comment notifications, due date reminders, or status change alerts)? And is this happening across your whole team, or have you only confirmed it with certain people?

---

### reporter

I've mostly noticed it with task assignments since that's what I rely on most, but now that you mention it, I think due date reminders have been late too — I got one that was supposed to warn me about a deadline and it came in after the deadline had already passed. I haven't really paid attention to comment notifications honestly. As for the team, I've heard a few other people complain about late notifications in our stand-ups, so it's not just me. I don't think anyone's done a systematic check though.

---

### triage-agent

This is really helpful — I think we're getting close to pinpointing this.

One more thing that would help confirm the pattern: have you noticed whether the notification delays also happen on weekends or days off, when presumably those daily digest emails aren't being sent? If notifications are fast on days without the digest and slow on days with it, that would pretty much confirm they're competing for the same email pipeline.

---

### reporter

Hmm, that's a good question. I don't usually work weekends so I'm not totally sure. But actually, I think I did get a notification on Saturday that came through pretty quickly — I remember being surprised because I was used to the delays by then. I don't think we get the digest emails on weekends but I'm not 100% certain about that. I'd have to check my inbox to be sure.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours due to suspected daily digest email queue saturation","problem":"Transactional email notifications (task assignments, due date reminders, and likely other types) are arriving 2-4 hours late instead of within 1-2 minutes. The delays are worst in the morning (9-10am) and improve through the afternoon. Multiple team members across a ~200-person organization are affected, causing missed deadlines.","root_cause_hypothesis":"A recently introduced daily digest email feature sends ~200 digest emails each morning around 9am. These digest emails likely share the same email sending queue/pipeline as transactional notifications. The burst of 200 digests saturates the queue, causing transactional notifications to back up behind them. As the digest backlog clears through the morning, transactional email latency gradually improves. This explains why weekends (no digests) appear to have normal delivery times.","reproduction_steps":["Observe the email sending queue around 9am on a weekday when daily digests are dispatched","Assign a task to a user at ~9:15am and measure notification delivery time","Compare delivery time to a task assigned at ~2pm the same day","Compare delivery time to a task assigned on a weekend (no digest emails)","Check email queue depth and processing rate before, during, and after the digest batch"],"environment":"TaskFlow instance serving ~200 users. Daily digest email feature was recently enabled (approximately one week ago). Specific email provider/queue system unknown — needs server-side investigation.","severity":"high","impact":"All ~200 team members are affected. Delayed task assignment notifications cause people to miss deadlines. Delayed due date reminders arrive after deadlines have passed, defeating their purpose. Team productivity and trust in the notification system are degraded.","recommended_fix":"1. Investigate whether digest and transactional emails share a single queue. If so, separate them into distinct queues or channels with transactional emails given higher priority. 2. Consider sending digest emails via a bulk/low-priority pathway. 3. Stagger digest delivery (e.g., spread over 30-60 minutes) rather than sending all 200 at once. 4. As an immediate mitigation, shift digest send time to off-peak hours (e.g., 6am or 7am) so the queue clears before the workday begins.","proposed_test_case":"After implementing the fix, send a batch of 200 digest emails at 9am and simultaneously trigger a task assignment notification. Verify the task notification arrives within 2 minutes. Repeat the measurement across morning, afternoon, and weekend windows to confirm consistent low latency.","information_gaps":["Exact email queue architecture (shared vs. separate queues, provider details) — requires server-side investigation","Whether the digest feature was the only deployment change ~1 week ago — check deployment logs","Whether comment notifications are also delayed (reporter only confirmed task assignments and due date reminders)","Exact weekend digest behavior (reporter believes digests don't send on weekends but isn't certain)"]}

---
