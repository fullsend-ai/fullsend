# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the timeline a bit better. You mentioned search used to be fast — can you recall roughly when you started noticing the slowdown? Did anything change around that time, like a TaskFlow update, a big import of tasks, or any changes to your setup? Also, when you say you have "a lot of tasks," could you give me a ballpark — are we talking hundreds, thousands, tens of thousands?

---

### reporter

Yeah, I think it started about 2 weeks ago. I'm pretty sure that lines up with when I upgraded to v2.3 — I don't remember doing anything else around that time. No big imports or anything like that. As for the number of tasks, I've been using TaskFlow for about 2 years now so I've got around 5,000 tasks in there.

---

### triage-agent

That's really helpful — the v2.3 timing is a strong clue. I'd love to understand a bit more about *which* searches feel slow. Is it every search that takes 10-15 seconds, or does it depend on what you're searching for? For example, do simple one-word searches feel different from longer or more specific queries? And are you using any filters (like status, date range, tags) when searching, or is it plain text search that's slow?

---

### reporter

Good question — now that I think about it, it's specifically when I search through task descriptions that it's slow. If I search by title only, it's still pretty fast. The plain text search across descriptions is where it bogs down. I don't usually use filters, just typing in a search term and searching across everything. Doesn't seem to matter if it's one word or multiple words, it's slow either way when it's searching descriptions.

---

### triage-agent

[RESOLVED] {"title":"Search across task descriptions regressed to ~10-15s after v2.3 upgrade (title-only search unaffected)","problem":"Since upgrading to TaskFlow v2.3, plain-text search that includes task descriptions takes 10-15 seconds to return results. Searching by title only remains fast. The user has approximately 5,000 tasks accumulated over 2 years. The slowdown is consistent regardless of query length or complexity.","root_cause_hypothesis":"The v2.3 release likely introduced a regression in description search — most probably a dropped or missing database index on the task descriptions column, a switch from indexed full-text search to a sequential scan (e.g., LIKE/ILIKE instead of FTS), or a query change that bypasses existing indexes. The fact that title search is unaffected suggests the title field still has proper indexing while the description field does not.","reproduction_steps":["Set up a TaskFlow instance running v2.3 with a dataset of ~5,000 tasks that have populated description fields","Perform a search using the default search mode (searching across all fields including descriptions)","Observe response time — expect 10-15 seconds","Perform the same search restricted to title only","Observe response time — expect sub-second results","Optionally repeat on v2.2 with the same dataset to confirm the regression"],"environment":"TaskFlow v2.3, work laptop (OS unspecified), ~5,000 tasks","severity":"high","impact":"Any user with a moderate-to-large task count who uses description search will experience significant delays. This is a core workflow — search is a primary navigation mechanism in a task management app. The 10-15 second wait degrades usability substantially.","recommended_fix":"1. Diff the search-related code and database migrations between v2.2 and v2.3 to identify changes to how description search is executed. 2. Check the query plan (EXPLAIN ANALYZE or equivalent) for description search queries to confirm whether an index is being used. 3. If an index was dropped or a migration failed to create one, add the appropriate full-text or trigram index on the descriptions column. 4. If the query itself changed (e.g., from FTS to LIKE), revert to an indexed search strategy.","proposed_test_case":"Create a performance regression test that populates a database with 5,000+ tasks with description text, executes a description search, and asserts the query completes within an acceptable threshold (e.g., under 2 seconds). Run this test as part of the CI pipeline for any changes to search functionality.","information_gaps":["Exact database backend in use (SQLite, PostgreSQL, etc.) — may affect index strategy","Whether other v2.3 users report the same issue or if it is data-shape dependent","Full v2.3 changelog to identify specific search-related changes"]}

---
