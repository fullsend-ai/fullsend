# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the full picture here.

Can you walk me through what you were doing when you noticed the slowdown? For example, what kind of search were you running — a simple keyword, filtering by tags/dates, or something else? And when you say it "used to be fast," do you have a sense of when things changed? Was it after an update, after your task list grew past a certain size, or did it seem to happen suddenly one day?

---

### reporter

Yeah so I mostly just search for keywords in my tasks. I noticed it got slow maybe 2 weeks ago? I think it was around when I updated to v2.3, but I'm not 100% sure if that's related or just a coincidence. I've been using TaskFlow for about 2 years and have a lot of tasks built up — probably around 5,000 or so. It was always snappy before though, even with all those tasks. The weird thing is that sometimes searches are fast and sometimes they're really slow, but I haven't figured out the pattern yet.

---

### triage-agent

That intermittent pattern is really interesting — let's try to tease it apart.

Think back to the last time a search was fast and the last time one was slow. What was different about those two moments? For instance: was it right after opening TaskFlow versus later in a session? Was it the first search you ran or a repeat of one you'd done before? Were you searching for a common word versus something specific? Anything you can remember about the circumstances around a fast search versus a slow one would be really helpful, even details that seem unrelated.

---

### reporter

Hmm, let me think... I think the fast ones might be when I'm searching for something that's in the task title? Like if I search for a project name that I use as a task title, it comes back quick. But when I'm looking for something that would be buried in the task details — like a person's name I mentioned in the description or notes I pasted in — that's when it crawls. I hadn't really connected those dots until you asked, but yeah, I think that might be the pattern. A lot of my tasks have really long descriptions because I paste meeting notes and stuff into them, some of them are pretty huge. Not sure if that matters.

---

### triage-agent

[RESOLVED] {"title":"Search slow (10-15s) when query matches task description/notes but fast for title matches — regression since ~v2.3","problem":"Keyword searches that match content in task descriptions or notes take 10-15 seconds to return results, while searches matching task titles remain fast. The reporter has ~5,000 tasks, many with large descriptions containing pasted meeting notes. Performance was consistently fast before approximately 2 weeks ago, correlating with an update to v2.3.","root_cause_hypothesis":"The v2.3 update likely changed search behavior to include task body/description fields without proper indexing, or dropped/broke an existing full-text index on those fields. The large volume of text in descriptions (pasted meeting notes) makes unindexed scans expensive, while title fields — shorter and possibly still indexed — remain fast.","reproduction_steps":["Create or use an account with a large number of tasks (~5,000), many containing lengthy descriptions","Search for a keyword that appears only in a task title — observe fast results","Search for a keyword that appears only in task descriptions or notes — observe 10-15 second delay","Compare query execution plans or logs for both searches"],"environment":"TaskFlow ~v2.3, running on a work laptop (OS and platform not specified), ~5,000 tasks accumulated over 2 years, many with large description fields","severity":"medium","impact":"Users with large task histories experience severe search slowdowns when queries need to match against task body content, making full-text search effectively unusable for its primary purpose. Users with fewer or shorter tasks may not notice.","recommended_fix":"1. Review v2.3 changelog for changes to search scope or query logic. 2. Check whether full-text indexes exist on task description/notes fields — add or rebuild them if missing. 3. If search was expanded to include body fields in v2.3, ensure those fields are indexed before being included in queries. 4. Consider adding pagination or query-time limits to prevent long scans.","proposed_test_case":"Create a dataset of 5,000+ tasks with varying description lengths (some with multi-KB pasted text). Benchmark search for a term appearing only in titles vs. a term appearing only in descriptions. Both should return results in under 1 second. Repeat after simulating a v2.2-to-v2.3 migration to verify no index regression.","information_gaps":["Exact TaskFlow version not confirmed (reporter said 'I think' v2.3)","Platform (web app vs desktop) and database backend not identified","Whether the reporter has any search filters or settings that might affect scope"]}

---
