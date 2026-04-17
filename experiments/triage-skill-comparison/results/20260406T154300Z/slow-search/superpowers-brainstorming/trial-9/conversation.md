# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down the cause, can you tell me which best describes the slowness?

1. **Every search is slow**, regardless of what I type
2. **Only certain search terms are slow** — simple or short queries are fine, but longer or more complex ones are slow
3. **The first search is slow**, but subsequent searches are faster
4. **It's intermittent** — the same search is sometimes fast and sometimes slow
5. Something else

Also, roughly how many tasks do you have in your TaskFlow instance? (e.g., hundreds, thousands, tens of thousands?)

---

### reporter

It's closest to option 2, but let me explain — searching by task title is still fast, it's specifically when I search across task descriptions that it gets really slow. Every description search is slow though, not just certain terms. I have about 5,000 tasks built up over the last couple years.

---

### triage-agent

[RESOLVED] {"title":"Search across task descriptions is slow (~10-15s) with ~5,000 tasks; title search unaffected","problem":"Searching by task description takes 10-15 seconds to return results, while searching by task title remains fast. The user has approximately 5,000 tasks accumulated over two years. The slowness is consistent for all description searches, not specific to certain query terms.","root_cause_hypothesis":"The task description column likely lacks a database index or full-text search index. Title search is fast because it hits an indexed column, while description search performs a full table scan or unoptimized pattern match (e.g., SQL LIKE '%term%') against 5,000 rows of potentially large text content.","reproduction_steps":["Create or use a TaskFlow instance with ~5,000 tasks that have populated description fields","Perform a search using a task title keyword — observe fast results","Perform a search using a keyword that appears in task descriptions but not titles — observe 10-15 second delay"],"environment":"Work laptop, ~5,000 tasks. Specific OS/DB/TaskFlow version not confirmed but unlikely to affect the fix approach.","severity":"medium","impact":"Users with large task counts experience unacceptable search latency when searching descriptions, degrading the core search feature. Title-only search still works, so search is not fully broken.","recommended_fix":"1. Check the query plan for description search queries (EXPLAIN/EXPLAIN ANALYZE). 2. Add a full-text search index on the task description column (e.g., GIN index with tsvector in PostgreSQL, or FULLTEXT index in MySQL). 3. Update the search query to use the full-text search capability instead of pattern matching. 4. If the application already uses an ORM, verify it generates indexed queries for description search. 5. Consider whether a combined title+description full-text index would be more appropriate.","proposed_test_case":"With a dataset of 5,000+ tasks with populated descriptions, assert that a description search query returns results in under 1 second. Include a performance regression test that fails if description search exceeds an acceptable threshold (e.g., 2 seconds).","information_gaps":["Exact database engine and version (affects index syntax but not the overall approach)","Whether search is implemented at the application layer or database layer","Whether this is a regression (reporter says it 'used to be fast') — could indicate a dropped index or a migration that changed the search implementation"]}

---
