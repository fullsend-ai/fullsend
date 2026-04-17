# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — slow search is definitely frustrating.

A few things that would help me narrow this down:

1. **When did the slowness start?** Was there a specific update, or did it degrade gradually? If you know roughly which version of TaskFlow you're on, that would be very helpful.
2. **How many tasks are we talking about?** Hundreds? Thousands? Tens of thousands?
3. **Is it slow for every search, or only certain queries?** For example, is searching for a single word just as slow as a more complex search?

If you can answer those three, I'll have a much better picture of what's going on.

---

### reporter

1. It started about 2 weeks ago. I upgraded from v2.2 to v2.3 right around that time, so it might be related to the update. 2. I have around 5,000 tasks — I've been using TaskFlow for about 2 years now. 3. Good question — I just tested this. Searching by task title is still fast, basically instant. But when I search across task descriptions, that's when it crawls. 10-15 seconds easy. Some of my tasks have really long descriptions (I paste meeting notes into them sometimes), not sure if that matters.

---

### triage-agent

[RESOLVED] {"title":"Search across task descriptions regressed to 10-15s in v2.3 (title search unaffected)","problem":"After upgrading from v2.2 to v2.3 (~2 weeks ago), searching across task descriptions takes 10-15 seconds. Searching by task title remains instant. The user has ~5,000 tasks accumulated over 2 years, some with very long descriptions (pasted meeting notes). This is a regression — description search was fast in v2.2.","root_cause_hypothesis":"v2.3 likely changed the description search implementation in a way that bypasses or removes the full-text index on the task descriptions column. Possible causes: (1) a migration dropped or failed to rebuild the full-text index on descriptions, (2) the search query was refactored to use an unoptimized pattern (e.g., LIKE '%term%' or application-level filtering instead of a database-level full-text search), or (3) a new ORM/query builder in v2.3 generates a different query plan that doesn't use the index. The fact that title search is unaffected suggests the title index is intact and only the description search path changed.","reproduction_steps":["Use a TaskFlow instance with a large number of tasks (~5,000) where some tasks have lengthy descriptions","Upgrade from v2.2 to v2.3","Perform a search that targets task descriptions (not just titles)","Observe 10-15 second response time vs. near-instant for title-only search"],"environment":"TaskFlow v2.3, running on a work laptop (OS and DB engine not specified but likely local/embedded database). ~5,000 tasks, some with very long description fields.","severity":"medium","impact":"Affects power users with large task databases who rely on description search. Title search still works as a partial workaround, but users who store detailed notes in descriptions lose effective searchability. Likely affects all v2.3 users at scale, not just this reporter.","recommended_fix":"1. Diff the search-related code and database migrations between v2.2 and v2.3. 2. Check whether a full-text index on the descriptions column exists in v2.3 databases — run EXPLAIN/ANALYZE on the description search query to confirm whether it's doing a sequential scan. 3. If the index was dropped, restore it (and add a migration). If the query changed, revert the description search query to use the indexed path. 4. For users who already upgraded, ensure the fix migration is idempotent (CREATE INDEX IF NOT EXISTS or equivalent).","proposed_test_case":"Create a test database with 5,000+ tasks, including tasks with description text >1KB. Run a description search and assert it completes in under 1 second. Run the same test with and without the full-text index to confirm the index is required for acceptable performance.","information_gaps":["Exact database engine (SQLite, PostgreSQL, etc.) — affects index type and fix approach","Whether the user ran any database migrations as part of the v2.3 upgrade","Server-side vs. client-side search architecture","Exact OS and hardware specs of the work laptop (likely not relevant given the regression pattern)"]}

---
