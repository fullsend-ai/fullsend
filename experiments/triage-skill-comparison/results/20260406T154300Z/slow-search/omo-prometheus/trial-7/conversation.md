# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — slow search is definitely frustrating.

A few specifics that would help me narrow this down: when did you first notice the slowdown? Did it coincide with anything — a TaskFlow update, a big import of tasks, or a change on your laptop? Also, roughly how many tasks are we talking about (hundreds, thousands, tens of thousands)? And is this the main task search bar, or a search/filter within a specific project or view?

---

### reporter

It started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3 actually. I have around 5,000 tasks — I've been using TaskFlow for about 2 years now. It's the main search bar. Before the upgrade it was always snappy, under a second. Now it just crawls.

---

### triage-agent

That's really helpful — the v2.3 upgrade timing is a strong signal. Two quick follow-ups to pin this down:

1. Is **every** search slow, or only certain queries? For example, is searching for a single word just as slow as a longer phrase? And does searching for a very common term behave differently than a rare one?
2. Is the **first** search after you open TaskFlow noticeably slower than searches after that, or is it consistently 10-15 seconds every time regardless?

This will help me figure out whether v2.3 broke the search index itself or the query path.

---

### reporter

Every search is slow, doesn't matter if it's one word or a longer phrase. Common or rare terms make no difference either — it's always that same painful 10-15 second wait. And it's consistent, not just the first search. Every single time.

Actually, now that I think about it, I should clarify something — searching by task *title* is still fast. It's when I search across task descriptions that it gets really slow. I have a lot of tasks with pretty long descriptions (I paste meeting notes into them sometimes), so maybe that's related?

---

### triage-agent

[RESOLVED] {"title":"Search across task descriptions regressed to ~10-15s in v2.3 (title search unaffected)","problem":"After upgrading from v2.2 to v2.3, searching via the main search bar takes 10-15 seconds consistently when results match on task descriptions. Title-only matches remain fast (<1s). The slowdown is consistent regardless of query complexity, term frequency, or whether it is the first search in a session. The user has ~5,000 tasks accumulated over 2 years, many with long descriptions (pasted meeting notes).","root_cause_hypothesis":"v2.3 likely dropped, broke, or failed to migrate the full-text search index on the task descriptions column. Alternatively, v2.3 may have changed the description search path from an indexed query to a full-table scan or application-level string matching. Title search still hits a working index, which explains why it remains fast while description search degrades linearly with data volume.","reproduction_steps":["Have a TaskFlow instance upgraded from v2.2 to v2.3 with a substantial number of tasks (~5,000) containing non-trivial descriptions","Open the main search bar","Search for any term that appears in task descriptions (not just titles)","Observe 10-15 second delay before results appear","Compare: search for a term that only appears in task titles — this should return quickly"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS/specs not specified but irrelevant given the regression is version-correlated), ~5,000 tasks with long descriptions","severity":"high","impact":"Any user with a non-trivial task count who searches by description content is affected. Search is a core workflow feature and 10-15s latency makes it effectively unusable. The regression affects all v2.3 users proportionally to their task/description volume.","recommended_fix":"1. Diff the database schema migrations between v2.2 and v2.3 — look for changes to full-text indexes on the tasks/descriptions table. 2. Check the search query execution plan in v2.3 for description search vs title search. 3. If the index was dropped or altered, restore it. If the query path changed (e.g., switched to LIKE '%term%' or application-level filtering), revert to the indexed approach. 4. Verify with a dataset of ~5,000+ tasks with long descriptions.","proposed_test_case":"Create a test database with 5,000+ tasks, each with description text of 500+ words. Benchmark main search bar queries that match on description content. Assert that search returns results in under 2 seconds. Run this test against both the v2.2 and v2.3 search code paths to confirm the regression and validate the fix.","information_gaps":["Exact database backend (SQLite, PostgreSQL, etc.) — affects index investigation approach","Whether other v2.3 users with large task counts report the same issue","v2.3 changelog/migration scripts (not yet examined by a developer)"]}

---
