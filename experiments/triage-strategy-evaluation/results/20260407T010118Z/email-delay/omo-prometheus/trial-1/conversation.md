# Conversation: email-delay x omo-prometheus

## Issue

**Email notifications arriving hours late**

Task assignment notifications used to arrive within a minute or two. For the past week, they've been arriving 2-4 hours late. This is causing people to miss deadlines because they don't know they've been assigned a task until hours later. It seems to be worse in the morning — notifications sent around 9-10am take the longest to arrive. Afternoon notifications are usually faster but still delayed.

---

## Comments

### triage-agent

Thanks for the detailed report — the timing pattern you noticed (worse around 9-10am, better in the afternoon) is a really useful clue.

To help narrow this down: do you know if this delay is affecting all users on your team, or have you only confirmed it on your own account? And did anything change about a week ago when this started — a new deployment, a change in your email provider, or perhaps a jump in the number of users or tasks being created around that time?

---

### reporter

Yeah, it's affecting everyone on my team — I've checked with a few people and they're all seeing the same delays. As for what changed, I'm not sure about deployments exactly, but I think there was some new feature added around that time... like a daily digest or newsletter thing? I started getting those marketing summary emails in my inbox around the same time the delays started. Not sure if that's related or just a coincidence. We haven't changed email providers or anything like that, and our team size has been the same.

---

### triage-agent

That daily digest/newsletter detail is really helpful — that could very well be related. Do you know roughly what time those digest emails arrive in your inbox? If they're landing around 9-10am, that could explain the congestion pattern you're seeing.

Also, when you notice a delayed notification, is there any indication of *when* it was actually sent? For example, does the email header or timestamp show it was sent on time but delivered late, or does it look like the system didn't even send it until hours after the task was assigned?

---

### reporter

Yeah, the digest emails come in around 9am every morning, so that does line up with when the delays are worst. As for the email timestamps — I haven't dug into the email headers or anything like that, but I did notice that when I finally get a late notification, the task assignment timestamp in TaskFlow shows it was created hours before the email came through. So it seems like the system is just slow to send them out, not like my email provider is holding them. I usually just check the TaskFlow dashboard now to see if I have new assignments, but that kind of defeats the purpose of notifications.

---

### triage-agent

Thanks — this is really coming together. Here's my working theory: the new daily digest feature is sending a large batch of emails around 9am every morning, and that batch is flooding the email queue. Your task assignment notifications get stuck behind all those digest emails, which is why morning notifications are delayed 2-4 hours but afternoon ones are faster (the queue has cleared by then).

Does that match what you're seeing? And one thing that would help confirm this: do you have a rough sense of how many people in your organization receive that daily digest? If it's going out to hundreds or thousands of users, that would explain why the queue backs up enough to cause multi-hour delays.

---

### reporter

Yeah, that theory makes a lot of sense to me — it matches exactly what we're experiencing. The morning delays lining up with the digest emails is too much of a coincidence. As for how many people get the digest, I'm not totally sure of the exact number, but we're a pretty big org — I think it goes out to most users? There are maybe a couple hundred people in our TaskFlow instance, so it could be a lot of emails going out at once. I imagine each person gets one digest, so that's at least a few hundred emails all at 9am on top of whatever normal notifications are being sent. Is there a way to prioritize the task assignment emails over the bulk digest stuff?

---

### triage-agent

[RESOLVED] {"title":"Task notification emails delayed 2-4 hours due to email queue congestion from daily digest feature","problem":"Since the daily digest/newsletter feature was introduced approximately one week ago, task assignment notification emails are arriving 2-4 hours late. Delays are worst in the morning (9-10am) and improve in the afternoon. All users on the reporter's team are affected, causing missed deadlines.","root_cause_hypothesis":"The daily digest feature sends a bulk batch of emails to all users (~hundreds) at 9am each morning. These digest emails flood the email sending queue, and task assignment notifications — which are time-sensitive — get stuck behind the bulk batch. The queue drains over 2-4 hours, after which afternoon notifications flow more normally.","reproduction_steps":["Have the daily digest feature enabled (default for the org)","Wait for 9am when the digest batch fires","Assign a task to a user shortly after 9am","Observe that the notification email arrives 2-4 hours later","Compare with a task assigned in the afternoon — notification arrives faster"],"environment":"TaskFlow instance with ~200 users, all receiving the daily digest. No recent changes to email provider or team size. Issue began approximately one week ago, coinciding with the digest feature launch.","severity":"high","impact":"All users in the organization experience delayed task assignment notifications, causing missed deadlines. Users are forced to manually check the TaskFlow dashboard as a workaround, undermining the purpose of email notifications.","recommended_fix":"Investigate the email sending queue architecture. Likely fixes in order of preference: (1) Implement priority lanes in the email queue so transactional emails (task assignments, mentions) are sent ahead of bulk emails (digests, newsletters). (2) Rate-limit or stagger the digest send over a longer window rather than enqueuing all at once. (3) Move digest emails to a separate queue or sending infrastructure so they don't compete with transactional notifications.","proposed_test_case":"Send a batch of N digest emails (simulating the 9am burst) and immediately enqueue a task assignment notification. Verify the task notification is delivered within the SLA (e.g., under 2 minutes) regardless of digest queue depth. Test with N matching production scale (~200+).","information_gaps":["Exact email queue implementation (single queue vs. multiple, queue technology used)","Whether the digest feature has any batching or rate-limiting configuration already","Precise number of digest recipients and emails per digest run","Server-side email sending logs confirming the queue backup theory"]}

---
