# Conversation: email-delay x omo-prometheus

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the time-of-day pattern is a useful clue.

To start narrowing this down: is this delay affecting **all users** on your team, or only specific people (e.g., certain email domains or roles)? And do you have any visibility into whether the notifications are being **generated late** by TaskFlow (e.g., do they show up late in the in-app notification list too) or whether TaskFlow sends them on time but the **emails arrive late**?

---

### reporter

It's affecting everyone on my team as far as I can tell — we're all on the same company email domain. I asked a couple people and they noticed the same thing.

As for whether it's TaskFlow or the email itself — I haven't checked the in-app notifications honestly. I mostly rely on email. Let me check... actually I think the in-app ones might be showing up on time? I'm not 100% sure but I recall seeing a task in my dashboard before I got the email about it. So it seems like TaskFlow is generating them fine but the emails are just slow to come through.

---

### triage-agent

Thanks for checking on that — the fact that in-app notifications seem timely is a really helpful data point. That tells us TaskFlow is generating notifications on time, but something in the email delivery pipeline is introducing the delay.

You mentioned this started about a week ago. Can you recall whether anything changed around that time — a TaskFlow upgrade or deployment, a change to your email provider or mail server configuration, or any new email-related settings (e.g., a new sending domain, rate limits, or spam filtering rules)? Even something that seemed unrelated could be the trigger. If you're not sure, who on your team would have visibility into the email sending infrastructure?

---

### reporter

Hmm, I'm not sure about any email server changes — I'm not really involved in that side of things. But now that you mention it, I think there was a new feature added around that time. We started getting these daily digest emails from TaskFlow, like a summary of activity or something marketing-related. I started seeing those about a week ago, which lines up with when the delays started. Not sure if that's related or just a coincidence. Our IT admin would probably know more about the email infrastructure side of things.

---

### triage-agent

That's a really useful connection — the timing of that new daily digest feature lining up with when delays started is suspicious.

Here's my working theory: if those digest emails are being sent out in bulk each morning (say, one per user), they could be flooding the email sending queue and pushing your task-assignment notifications to the back of the line. That would explain why mornings are worst (digests + real notifications competing) and afternoons are better (digest backlog has cleared).

Can you confirm roughly what time those daily digest emails tend to arrive in your inbox? If they're landing around 9-10am — right when you see the worst delays — that would strongly support this theory.

---

### reporter

Yeah, the digest emails usually show up around 9am or shortly after, which is exactly when the delays are worst. That does seem like more than a coincidence now that you lay it out like that. I think everyone in the company gets one of those digests too, so that's probably a lot of emails going out at once.

---

### triage-agent

[RESOLVED] {"title":"Email notifications delayed 2-4 hours due to queue saturation from new daily digest feature","problem":"Task assignment email notifications that previously arrived within 1-2 minutes are now delayed by 2-4 hours. The delay is worst in the morning (9-10am) and less severe in the afternoon. In-app notifications are unaffected, confirming the issue is in the email delivery pipeline, not notification generation.","root_cause_hypothesis":"The recently introduced daily digest email feature (rolled out ~1 week ago) sends bulk emails to all company users around 9am. These digest emails are likely sharing the same email sending queue as transactional task-assignment notifications. The high volume of digest emails saturates the queue each morning, pushing time-sensitive assignment notifications to the back of the line. As the digest backlog clears through the day, afternoon notifications experience shorter delays.","reproduction_steps":["Wait for daily digest emails to begin sending (around 9am)","Assign a task to a user in TaskFlow shortly after 9am","Observe that the in-app notification appears immediately","Observe that the email notification arrives 2-4 hours later","Repeat the same assignment in the afternoon and observe a shorter delay"],"environment":"All users on the same company email domain. TaskFlow instance with the daily digest feature enabled (deployed approximately one week ago). Specific email sending infrastructure unknown — IT admin has visibility.","severity":"high","impact":"All users company-wide are affected. Team members are missing task assignment deadlines because they rely on email notifications and don't see assignments for hours. Direct productivity and deadline impact.","recommended_fix":"1. Separate the email sending queues: transactional notifications (task assignments, mentions, etc.) should use a higher-priority queue than bulk/marketing emails (daily digests). 2. As an immediate mitigation, consider rate-limiting or staggering the digest send (e.g., spread over 30-60 minutes rather than bursting all at once). 3. Alternatively, move digest emails to a dedicated sending worker/queue so they never compete with transactional email. 4. Review the email provider's rate limits — the burst of digests may also be triggering provider-side throttling.","proposed_test_case":"Send a task assignment notification while a batch of digest emails is being queued. Verify the assignment notification is delivered within 2 minutes regardless of digest queue depth. Additionally, verify that moving digest emails to a separate queue or priority lane eliminates the morning delay pattern.","information_gaps":["Exact email sending infrastructure (SMTP server, third-party service like SendGrid/SES, queue implementation)","Exact number of digest recipients (company size) to quantify queue load","Whether the email provider is applying rate limiting or throttling during the digest burst","Whether digest and transactional emails already use separate queues or share one"]}

---
