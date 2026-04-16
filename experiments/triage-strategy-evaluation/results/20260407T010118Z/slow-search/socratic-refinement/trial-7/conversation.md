# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd love to understand the context a bit better.

Can you walk me through what you were doing when you noticed the slowdown? For example, what kind of search were you running — a simple keyword, filtering by tags or dates, or something else? And when you say it "used to be fast," do you have a sense of when things changed? Was it gradual, or did it feel like it happened suddenly after a particular event (like an update, importing a bunch of tasks, etc.)?

---

### reporter

Yeah, so I mostly just type keywords into the search bar to find old tasks. I noticed it got slow maybe two weeks ago? I think it was around when I updated to v2.3, but I'm not totally sure if that's related. It definitely felt like it happened pretty suddenly though, not a gradual thing. I've been using TaskFlow for about 2 years and have a lot of tasks built up — probably around 5,000 or so. The weird thing is that sometimes searches are fine and sometimes they're super slow, but I haven't really figured out the pattern yet.

---

### triage-agent

That intermittent pattern is really interesting — it's often the best clue for tracking down performance issues.

Can you think of any differences between the searches that come back quickly and the ones that drag? For instance, are the slow ones searching for more common words, or longer phrases? Or does it seem more like it depends on *when* you search — like it's slow first thing in the morning but faster later, or slow right after opening the app? I'm curious whether the slowness follows the *what* you search for or the *when* you search.

---

### reporter

Hmm, now that you mention it, I think it might be more about *what* I'm searching for. Like, when I search for a task and the keyword is in the title, it seems to come back pretty quick. But when I'm trying to find something that I know is buried in the description of a task, that's when it really drags. I have a bunch of tasks where I pasted in meeting notes and stuff so some of those descriptions are pretty long. I hadn't really thought about it that way until you asked, but yeah, I think that's the pattern.

---

### triage-agent

That's a really helpful observation — I think we're zeroing in on it.

So here's what I'm hearing: searches that only need to match against task titles come back fast, but when the match is buried in a task's description — especially the longer ones with pasted meeting notes — that's when it bogs down. And this started around the v2.3 update about two weeks ago.

Does that match your experience? And one thing I'm curious about: before the v2.3 update, do you recall whether search *used to* find things in descriptions at all? I'm wondering if searching inside descriptions might be a new feature that v2.3 introduced, or if it always did that and just got slower. That distinction would really help narrow down where the problem is.

---

### reporter

Yeah, that summary sounds right. And to answer your question — no, searching descriptions isn't new. It definitely worked before in v2.2, and it was fast too. I used to search for stuff in my meeting notes all the time and it would come back almost instantly. So it's not a new feature, it just got way slower after the update.

---

### triage-agent

[RESOLVED] {"title":"Search regression in v2.3: description-field searches take 10-15 seconds (title searches unaffected)","problem":"After updating to v2.3, keyword searches that match against task descriptions take 10-15 seconds to return results. Searches that match against task titles remain fast. The user has ~5,000 tasks, many with long descriptions containing pasted meeting notes. This was not an issue in v2.2 — description searches returned almost instantly.","root_cause_hypothesis":"The v2.3 update likely introduced a regression in how description fields are searched — most probably a dropped or unused full-text index on the description column, a change from indexed search to sequential scanning, or a query planner regression. The fact that title searches are fast suggests the title index is intact while the description search path changed.","reproduction_steps":["Set up a TaskFlow instance on v2.3 with a large dataset (~5,000 tasks), including tasks with long description fields","Search for a keyword that appears only in a task title — observe fast response","Search for a keyword that appears only in a task description (not the title) — observe 10-15 second delay","Optionally repeat on v2.2 to confirm the regression"],"environment":"TaskFlow v2.3, work laptop (OS unspecified), ~5,000 tasks accumulated over 2 years, many tasks with long descriptions containing pasted meeting notes","severity":"high","impact":"Any user with a non-trivial number of tasks who searches for content in task descriptions experiences severe slowdowns, effectively breaking a core workflow. The problem scales with dataset size and description length.","recommended_fix":"Diff the search implementation between v2.2 and v2.3. Specifically investigate: (1) whether a full-text index on the description field was dropped or is no longer being used, (2) whether the search query was refactored in a way that bypasses indexing (e.g., switching from indexed FTS to LIKE/ILIKE scanning), (3) whether a migration in v2.3 failed to rebuild or maintain the description search index. Check query execution plans for description-matching searches.","proposed_test_case":"Create a performance regression test: seed a database with 5,000+ tasks (some with long descriptions >1KB), then assert that a keyword search matching only description content returns results within an acceptable threshold (e.g., <1 second). Run this test against both v2.2 and v2.3 to confirm the regression and validate any fix.","information_gaps":["Exact database backend in use (SQLite vs PostgreSQL vs other)","Whether the user is on desktop app or web version","Specific OS and hardware details of the work laptop","Whether other v2.3 users have reported the same issue"]}

---
