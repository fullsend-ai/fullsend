# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — slow search is definitely frustrating.

A couple of things that would really help narrow this down: **When did you first notice the slowdown?** Was there a specific update, or did it degrade gradually? And when you say you have "a lot of tasks," can you give me a rough number? (Hundreds? Thousands?) Also, does the slowness happen on every search, or only with certain search terms or filters?

Knowing whether this is a sudden regression vs. gradual degradation will point us in very different directions.

---

### reporter

It started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3 — so I'm pretty sure it was sudden, not gradual. I have around 5,000 tasks built up over the last couple years. The slowness seems to happen every time I search, though now that I think about it, it's specifically when I'm searching through task descriptions. If I just search by title it's still quick. But yeah, searching descriptions is painfully slow now, like 10-15 seconds every time.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s query time with ~5k tasks","problem":"After upgrading from v2.2 to v2.3, searching by task description takes 10-15 seconds consistently. Title-only search remains fast. The user has approximately 5,000 tasks accumulated over two years.","root_cause_hypothesis":"The v2.3 release likely introduced a change to the description search path — most probably a dropped or missing database index on the task descriptions column, a switch from indexed full-text search to a naive LIKE/ILIKE scan, or a removed search optimization (e.g., search cache or pre-computed index). The fact that title search is unaffected and description search is uniformly slow (not intermittent) points to a query-plan regression rather than a resource contention issue.","reproduction_steps":["Create or use an account with a large number of tasks (~5,000)","Upgrade from TaskFlow v2.2 to v2.3","Perform a search using a term that matches task descriptions","Observe 10-15 second response time","Perform a search using a term that matches only task titles","Observe that title search returns quickly"],"environment":"TaskFlow v2.3, work laptop (OS/specs not specified), ~5,000 tasks","severity":"high","impact":"Any user with a moderate-to-large task count who searches by description is affected. Search is a core workflow feature, and 10-15s latency effectively breaks it. Workaround exists (search by title only) but significantly limits functionality.","recommended_fix":"1. Diff the search-related code and database migrations between v2.2 and v2.3 — look for changes to description search queries, removed indexes, or altered full-text search configuration. 2. Run EXPLAIN/ANALYZE on the description search query against a dataset with ~5k tasks to confirm whether it's doing a sequential scan. 3. If an index was dropped or a migration missed, restore it. If the search implementation changed, benchmark the new approach against the old one at scale.","proposed_test_case":"Performance test: seed a test database with 5,000+ tasks with varied descriptions. Assert that a description search query returns results in under 1 second (or whatever the pre-v2.3 baseline was). Include this as a regression test in CI.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be the cause given the regression pattern)","Whether the database is local (SQLite) or remote (PostgreSQL, etc.) — affects which index types to investigate","Whether other users on v2.3 with large task counts experience the same issue"]}

---
