# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down what's going on, could you tell me what kind of search you're typically doing when you notice the slowdown?

1. **Text search** — typing keywords to find tasks by title or description
2. **Filtering/faceted search** — using filters like status, assignee, due date, etc.
3. **A combination** — typing keywords while also applying filters
4. **Something else**

Also, when you say it "used to be fast," do you recall roughly when it started slowing down — for example, after an update, or after your task list grew past a certain size?

---

### reporter

It's mainly text search — I search by keywords and it looks through task descriptions. That's where it's really slow. If I just search by title it's actually still pretty fast, now that I think about it.

As for when it started... I think it was maybe 2 weeks ago? I upgraded to v2.3 around that time so it might be related to that, but I'm not 100% sure.

---

### triage-agent

Thanks, that distinction between title search and description search is really helpful — it narrows things down a lot.

Here's what I think is going on: **the v2.3 upgrade likely introduced a regression in how description text is searched** — for example, a missing or changed database index, or a switch from indexed search to a full-text scan over descriptions. Title search still being fast supports this, since titles are shorter and likely indexed differently.

Does that match your experience? Specifically:

1. **Yes, that sounds right** — it's consistently slow on description search regardless of how many results come back
2. **Not quite** — it's only slow when there are a lot of matching results
3. **Not sure** — but I can add that it also happens on a fresh restart / in a new browser session (i.e., it's not a caching or memory issue)

This will help confirm whether we're looking at a query/indexing regression versus something else like a memory or rendering bottleneck.

---

### reporter

Yeah, option 1 sounds right. It's slow every time I search descriptions, even if only a couple results come back. It doesn't seem to matter what I search for — it's just always slow now. I also noticed my laptop's fan kicks on when I do a search, if that helps at all.

---

### triage-agent

[RESOLVED] {"title":"Description text search regression in v2.3 — 10-15s query time with high CPU usage","problem":"Since upgrading to v2.3 (~2 weeks ago), searching tasks by description keywords takes 10-15 seconds and causes high CPU usage (laptop fan spins up). Searching by title remains fast. The slowdown is consistent regardless of query terms or number of results returned.","root_cause_hypothesis":"The v2.3 release likely introduced a change to how description text is searched — most probably a dropped or altered full-text index on the description field, or a code change that bypasses the index (e.g., switching from an indexed query to an in-application substring scan or unindexed LIKE/regex query). The high CPU and independence from result count are consistent with a full table scan over description text.","reproduction_steps":["Install or upgrade to TaskFlow v2.3","Have a non-trivial number of tasks with descriptions","Perform a text search using keywords that match task descriptions","Observe ~10-15 second response time and elevated CPU usage","Perform the same keyword search restricted to titles only — observe it completes quickly"],"environment":"TaskFlow v2.3, reporter's work laptop (specific OS/DB not confirmed but likely irrelevant given this appears to be a code-level regression)","severity":"high","impact":"All users performing description text search on v2.3 are likely affected. Search is a core workflow for task management, and 10-15s delays make it effectively unusable for iterative searching.","recommended_fix":"Diff the description search code path between v2.2 and v2.3. Check for: (1) dropped or altered database indexes on the description column, (2) query changes that bypass full-text indexing (e.g., LIKE '%term%' replacing a full-text search call), (3) new in-application filtering that loads all descriptions into memory. Run EXPLAIN/ANALYZE on the description search query to confirm whether an index is being used.","proposed_test_case":"Create a dataset with 10,000+ tasks with multi-paragraph descriptions. Benchmark description keyword search and assert it completes in under 1 second. Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and later verify the fix.","information_gaps":["Exact number of tasks in the reporter's instance (not critical — slowness is consistent regardless of result count)","Specific database engine in use (developer can determine from project config)","Whether other v2.3 users report the same issue (likely yes given it appears to be a code regression)"]}

---
