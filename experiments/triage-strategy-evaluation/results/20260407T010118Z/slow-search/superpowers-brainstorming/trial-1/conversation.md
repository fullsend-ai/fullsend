# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down the cause, can you tell us what kind of search you're typically doing when you see the slowness?

a) **Simple keyword search** — typing a word or phrase into the main search bar
b) **Filtered search** — using filters like status, assignee, date range, etc.
c) **Full-text search** — searching within task descriptions/comments
d) **Something else**

Also, roughly how many tasks are in your TaskFlow instance?

---

### reporter

It's mostly (c) — I search within task descriptions a lot. I have around 5,000 tasks at this point, been using TaskFlow for a couple years now. The weird thing is that searching by title still seems fast, it's just when I'm searching through descriptions that it crawls. I'd say I have maybe 10-15 seconds wait each time.

---

### triage-agent

[RESOLVED] {"title":"Full-text search over task descriptions is slow (~10-15s) at ~5,000 tasks","problem":"Searching within task descriptions takes 10-15 seconds to return results for a user with ~5,000 tasks. Title-based search remains fast. The regression suggests the description search path lacks proper indexing or is performing unoptimized full-table scans.","root_cause_hypothesis":"The task descriptions column is missing a full-text search index (or the existing index has not been updated/rebuilt as the dataset grew). Title search likely hits an indexed column, while description search falls back to a sequential scan or LIKE query over unindexed text.","reproduction_steps":["Create or use a TaskFlow instance with ~5,000 tasks that have non-trivial descriptions","Perform a full-text search (search within descriptions) for a common term","Observe query latency of ~10-15 seconds","Compare with a title-only search for the same term — should return quickly"],"environment":"Work laptop, long-running TaskFlow instance (~2 years, ~5,000 tasks). Specific OS and database engine not confirmed but not needed to begin investigation.","severity":"medium","impact":"Users with large task counts experience significant delays on description search, degrading daily usability. Title search is unaffected.","recommended_fix":"1. Check the database schema for the tasks table — verify whether a full-text index exists on the description column. 2. If missing, add an appropriate full-text index (e.g., GIN index with tsvector for PostgreSQL, FULLTEXT index for MySQL). 3. Review the search query itself — ensure it uses the index rather than LIKE '%term%'. 4. Consider adding pagination or result-count limits if not already present.","proposed_test_case":"With a dataset of 5,000+ tasks with substantive descriptions, assert that a full-text description search completes in under 2 seconds. Benchmark before and after the index change.","information_gaps":["Exact database engine in use (PostgreSQL, MySQL, SQLite) — affects index syntax but not the diagnosis","Whether the slowness appeared suddenly (possible regression) or grew gradually with data volume","TaskFlow version"]}

---
