# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — slow search is definitely frustrating.

A couple of things that would really help narrow this down: **When did you first notice the slowdown?** Was there a specific update, or did it degrade gradually? And when you say you have "a lot of tasks," can you give me a rough number — are we talking hundreds, thousands, tens of thousands?

Also, does the slowdown happen on every search, or only with certain queries (e.g., longer search terms, specific filters)?

---

### reporter

It started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3 actually. I have around 5,000 tasks — I've been using TaskFlow for about 2 years now. It seems to happen when I'm searching through task descriptions. Like if I search for something I know is in a task's description it's really slow, but searching by title seems fine, those come back quick. It happens every time I do a description search, doesn't matter what I search for.

---

### triage-agent

[RESOLVED] {"title":"Search by task description regressed to 10-15s in v2.3 (title search unaffected)","problem":"After upgrading from v2.2 to v2.3, searching tasks by description takes 10-15 seconds consistently, regardless of query. Searching by title remains fast. The user has approximately 5,000 tasks accumulated over 2 years of use.","root_cause_hypothesis":"The v2.3 release likely introduced a regression in description search — most probably a dropped or missing database index on the task descriptions field, a change from indexed full-text search to an unoptimized pattern match (e.g., unindexed LIKE/ILIKE), or a query planner change causing full table scans on descriptions. The fact that title search is unaffected suggests titles are still indexed while descriptions lost their optimization.","reproduction_steps":["Create or use an account with a large number of tasks (~5,000)","Run TaskFlow v2.3","Perform a search using a term known to exist in a task description","Observe response time of 10-15 seconds","Perform a search using a term known to exist in a task title","Observe that title search returns quickly"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop, ~5,000 tasks","severity":"high","impact":"Any user with a non-trivial number of tasks who searches by description is affected. Description search is likely a core workflow for power users. Title-only search is a partial workaround but inadequate for users who need to find tasks by content.","recommended_fix":"1. Diff the search implementation between v2.2 and v2.3 — look for changes to the description search query, ORM calls, or database migrations affecting the tasks table. 2. Check for dropped or missing indexes on the description column. 3. Run EXPLAIN/ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm whether it's doing a sequential scan. 4. Restore or add the appropriate index (full-text index if using FTS, or a GIN/trigram index for pattern matching).","proposed_test_case":"Performance regression test: seed a database with 5,000+ tasks with varied descriptions, execute a description search query, and assert response time is under 1 second. Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.","information_gaps":["Exact database engine in use (SQLite, PostgreSQL, etc.) — though the fix approach is similar regardless","Whether any v2.3 database migration explicitly altered the tasks table schema or indexes","Server-side vs. client-side timing breakdown (is the delay in the query, the API, or rendering)"],"confidence":{"root_cause":"high","reproduction":"high","severity_assessment":"high"}}

---
