# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — that kind of slowdown is definitely frustrating.

Can you help me understand the timeline a bit? You mentioned search used to be fast — do you recall roughly when it started getting slow? And did anything change around that time — like a TaskFlow update, a big import of tasks, a change in how you organize or tag things, or anything else that might have coincided with the slowdown?

---

### reporter

Yeah, now that you mention it, it started about 2 weeks ago. I'm pretty sure that lines up with when I upgraded from v2.2 to v2.3. I don't recall importing a ton of new tasks around that time or changing anything else — just the upgrade.

---

### triage-agent

That's really helpful — the v2.3 upgrade timing is a strong clue.

I'd love to understand the pattern of the slowness a bit more. When you search, is it consistently slow every time regardless of what you search for, or have you noticed it being worse with certain kinds of searches — like searching for specific words vs. filtering by tags, dates, or status? And when you say you have a lot of tasks, could you give me a rough sense of scale — are we talking hundreds, thousands, tens of thousands?

That'll help us figure out whether this is likely a broad regression in v2.3 or something more specific.

---

### reporter

Good question — now that I think about it, searching by task title is still pretty snappy. It's when I search for words that are in the task descriptions that it gets really slow. I don't use tags much so I can't speak to that. As for scale, I've got about 5,000 tasks built up over the last couple years. Some of them have pretty lengthy descriptions — I tend to paste in meeting notes and stuff, so some descriptions are quite long.

---

### triage-agent

[RESOLVED] {"title":"Full-text search on task descriptions extremely slow after v2.3 upgrade (10-15s, ~5k tasks)","problem":"After upgrading from v2.2 to v2.3 approximately two weeks ago, searching for words within task descriptions takes 10-15 seconds. Searching by task title remains fast. The user has ~5,000 tasks, many with lengthy descriptions containing pasted meeting notes.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in full-text indexing of task descriptions — either the index was dropped/not migrated, the search query was changed to bypass the index, or description search was switched to a non-indexed scanning approach (e.g., LIKE/regex scan instead of a full-text index lookup). The fact that title search is unaffected points to a change specific to description-field search, not a general query performance issue.","reproduction_steps":["Set up a TaskFlow instance with ~5,000 tasks, including tasks with lengthy descriptions (multiple paragraphs)","Perform a search for a word known to appear in task descriptions (not titles)","Observe that the search takes 10-15 seconds to return results","Perform a search for a word that appears in task titles","Observe that title search returns results quickly","Optionally: repeat on a v2.2 instance to confirm the regression"],"environment":"TaskFlow v2.3, upgraded from v2.2. Running on a work laptop (OS and specs not specified). Dataset: ~5,000 tasks with lengthy descriptions.","severity":"high","impact":"Any user with a non-trivial number of tasks who searches by description content experiences major delays. This is a core workflow regression introduced in the current release. Users with large or text-heavy datasets are most affected.","recommended_fix":"Diff the search query path and database migration scripts between v2.2 and v2.3. Check whether the full-text index on the task descriptions column still exists and is being used by the query planner (e.g., EXPLAIN ANALYZE the search query). If the index was dropped or altered in a migration, restore it. If the query was changed to bypass the index, revert or fix the query. Verify that any new search features in v2.3 (e.g., new search syntax, new fields) didn't inadvertently change the execution plan for description searches.","proposed_test_case":"Create a test with 5,000+ tasks with multi-paragraph descriptions. Assert that a full-text search on description content returns results within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.","information_gaps":["Exact laptop OS and hardware specs (unlikely to be root cause given the v2.3 correlation)","Whether tag-based or date-filtered searches are also affected","TaskFlow's database backend (SQLite, PostgreSQL, etc.) — relevant for understanding indexing specifics","Whether other v2.3 users with smaller datasets also experience slowness (would help confirm the index hypothesis vs. a query-complexity issue)"]}

---

