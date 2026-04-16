# Conversation: email-delay x socratic-refinement

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you've noticed (worse in the morning, better in the afternoon) is really helpful.

You mentioned this started about a week ago and notifications used to arrive within a minute or two. Can you think of anything that changed around that time? For example, did your team grow, did you start using a new workflow or integration, or did you notice any other changes in TaskFlow's behavior around the same period? Even things that seem unrelated could be useful here.

---

### reporter

Hmm, let me think. Our team size hasn't changed recently. I don't think we added any new integrations either. One thing I did notice is that around the same time, we started getting these daily digest emails from TaskFlow — like a marketing summary or something? They show up in the morning. I'm not sure if that's related but the timing does line up with when the delays are worst. Other than that, I can't think of anything obvious that changed.

---

### triage-agent

That's a really interesting observation about the daily digest emails — the timing correlation is suspicious. I'd like to pull on that thread a bit.

When you see those digest emails arrive in the morning, is it roughly the same time window (9-10am) when you notice the assignment notifications are most delayed? And do you happen to know if the digest emails are going out to your whole team or organization, or just to you individually?

Also, one more thing that would help narrow this down: does TaskFlow show you in-app notifications (like a bell icon or notification panel) when tasks are assigned? If so, do those appear on time, or are they delayed too? That would help us figure out whether this is specifically an email delivery bottleneck or something broader.

---

### reporter

Yeah, the digest emails usually hit my inbox around 9am, which is right when the delays are worst. I believe they go out to everyone in the organization — so that's like 200 people? Not sure exactly.

Good question about in-app notifications. I actually do see the bell icon update pretty much right away when I'm logged in. It's just the email notifications that are delayed. So yeah, seems like an email thing specifically.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours, likely due to bulk digest emails saturating shared send queue","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now delayed 2-4 hours. The delays are worst in the morning (9-10am) and less severe in the afternoon. In-app notifications are unaffected — only email delivery is delayed.","root_cause_hypothesis":"A recently-introduced daily digest email feature sends bulk emails to the entire organization (~200 users) at approximately 9am. These bulk sends likely share the same email queue or sending pipeline as transactional notifications (task assignments). The burst of ~200 digest emails saturates the queue or hits rate limits on the email provider, causing transactional emails to queue behind them. As the digest backlog clears through the morning, transactional email latency improves, explaining the afternoon recovery pattern.","reproduction_steps":["Assign a task to a user at approximately 9:00-9:15am (shortly after daily digests are dispatched)","Note the time the in-app notification appears (expected: immediate)","Note the time the email notification arrives (expected: 2-4 hour delay)","Repeat the same assignment around 3-4pm and compare email delivery latency (expected: shorter delay)","Check the email sending queue depth or logs at 9am vs afternoon to confirm backlog correlation"],"environment":"Organization with ~200 users. Daily digest email feature recently enabled. Exact email service provider and queue infrastructure unknown — developer should check.","severity":"high","impact":"All users in the organization are affected. Delayed assignment notifications cause people to miss task deadlines because they don't learn about assignments for hours. Morning assignments are most impacted, which is typically when most work is distributed.","recommended_fix":"1. Investigate whether digest emails and transactional notifications share an email queue or sending pipeline. 2. If shared, separate them: use a dedicated high-priority queue/channel for transactional emails (assignment notifications) and a lower-priority queue for bulk sends (digests). 3. If using an external email provider, check whether the digest burst is hitting rate limits that then throttle all subsequent sends. 4. Consider spreading the digest send over a longer window (e.g., stagger over 30 minutes) rather than sending all ~200 at once. 5. As an immediate mitigation, consider shifting the digest send time to off-peak hours (e.g., 7am or 11pm).","proposed_test_case":"Send a batch of 200 digest-equivalent emails followed immediately by a transactional notification email. Verify the transactional email arrives within the SLA (e.g., under 2 minutes) regardless of the bulk send. After the fix, repeat at 9am in production and confirm assignment notifications arrive within 1-2 minutes.","information_gaps":["Exact email service provider and queue architecture (developer can determine from codebase/infrastructure)","Whether other users in the organization experience the same delays or just the reporter (very likely systemic given the hypothesis, but not explicitly confirmed)","Exact date the digest feature was enabled vs. exact date delays began"]}

---
