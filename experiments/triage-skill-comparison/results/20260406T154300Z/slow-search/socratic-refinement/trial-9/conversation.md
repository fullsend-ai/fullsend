# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the timeline a bit better. You mentioned search used to be fast — can you recall roughly when you first noticed it slowing down? Did anything change around that time, like a TaskFlow update, a big import of tasks, or a change in how you're using search?

Also, when you say you have "a lot of tasks," do you have a rough sense of the number — are we talking hundreds, thousands, more?

---

### reporter

Yeah, I think it started about 2 weeks ago. I updated to v2.3 right around that time, so that could be related. I didn't do any big import or anything like that. I've got around 5,000 tasks built up over the last couple years, which is a lot, but search was always snappy before the update.

---

### triage-agent

That's really helpful — the v2.3 timing is a strong clue. I'd love to understand a bit more about what the slowness looks like in practice. When you search, is it slow every single time regardless of what you type, or have you noticed it's worse with certain kinds of searches? For example, does searching for a single word feel different from using filters like dates, tags, or project names? And when results do come back, are they at least accurate, or are you seeing unexpected results too?

---

### reporter

Good question — now that you mention it, I think it's specifically slow when I'm searching through task descriptions. Like if I search for a keyword that's in the body of a task, that's when it crawls. But if I search for something that's in a task title, it comes back pretty much instantly like it used to. I hadn't really put that together until you asked. The results do seem accurate when they finally come back, just takes forever. Some of my tasks have really long descriptions — I paste meeting notes into them, sometimes pretty lengthy ones.

---

### triage-agent

[RESOLVED] {"title":"Search on task descriptions is extremely slow after v2.3 update (title search unaffected)","problem":"After updating to TaskFlow v2.3, searching by keywords that appear in task descriptions/body text takes 10-15 seconds to return results. Searching by task title remains instant. The user has approximately 5,000 tasks, many with lengthy descriptions containing pasted meeting notes. Results are accurate when they return — the issue is purely performance.","root_cause_hypothesis":"The v2.3 update likely introduced a regression in how task description text is searched. Probable causes: (1) a full-text search index on the description/body field was dropped or not migrated during the v2.3 upgrade, (2) v2.3 changed the search query to perform unindexed full-table scans on description content, or (3) v2.3 introduced a new search code path for body text that bypasses the existing index. The fact that title search remains fast suggests the title index is intact and the issue is isolated to the description field's query or index.","reproduction_steps":["Set up a TaskFlow instance with a substantial number of tasks (ideally ~5,000) where many tasks have lengthy description text","Update to v2.3","Search for a keyword that appears only in task descriptions/body text — observe 10-15 second response time","Search for a keyword that appears in a task title — observe near-instant response","Compare query plans or database logs between the two searches to confirm indexing difference"],"environment":"TaskFlow v2.3, work laptop (specific OS/specs unknown), ~5,000 tasks with lengthy descriptions","severity":"high","impact":"Any user with a non-trivial number of tasks who searches by description content will experience severe slowdowns after upgrading to v2.3. This degrades a core workflow — search — from instant to 10-15 seconds, making it functionally unusable for body-text searches.","recommended_fix":"1. Diff the v2.3 database migration scripts against v2.2 to check whether the full-text index on the task description/body column was dropped or altered. 2. Inspect the v2.3 search query code path for description searches — check whether it still uses the index or falls back to LIKE/unindexed scan. 3. If the index was dropped, add a migration to restore it. If the query changed, restore the indexed query path. 4. Run EXPLAIN/ANALYZE on the slow description search query to confirm the fix uses the index.","proposed_test_case":"Create a performance regression test: seed a database with 5,000+ tasks with multi-paragraph descriptions, execute a description-text search, and assert the query completes within an acceptable threshold (e.g., under 1 second). Run this test against both v2.2 (baseline) and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be the root cause given the title-vs-description split)","Whether the database backend is SQLite, PostgreSQL, or another engine (affects index investigation approach)","Whether other v2.3 users with smaller datasets also experience the slowdown (would help confirm it's index-related vs. dataset-size-related)"]}

---
