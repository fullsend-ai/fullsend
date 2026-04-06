# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down the cause, do you have a sense of when the slowdown started?

1. **After a recent TaskFlow update** — it was fast one day and slow the next
2. **Gradually over time** — it got slower as you added more tasks
3. **Not sure** — you just noticed it recently

Also, roughly how many tasks are we talking about — hundreds, thousands, or tens of thousands?

---

### reporter

It was definitely after I updated to v2.3 about two weeks ago. Before that, search was snappy — under a second. Now it crawls. I have around 5,000 tasks built up over the last couple years.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3: queries take 10-15s (previously <1s)","problem":"After updating to TaskFlow v2.3, search queries that previously returned results in under one second now take 10-15 seconds. The user has approximately 5,000 tasks accumulated over two years.","root_cause_hypothesis":"A change in v2.3 likely introduced a performance regression in the search path — possible causes include a missing or dropped database index, a switch from indexed search to full table scan, removal of query result caching, or a change in the search algorithm (e.g., adding unoptimized full-text matching across all fields).","reproduction_steps":["Set up a TaskFlow instance with ~5,000 tasks (or use a seeded test dataset of equivalent size)","Run the same search query on v2.2.x and confirm sub-second response","Upgrade to v2.3 and run the same search query","Observe response time of 10-15 seconds"],"environment":"TaskFlow v2.3, ~5,000 tasks, work laptop (specific OS and hardware not provided but unlikely to be relevant given the version correlation)","severity":"high","impact":"Any user with a non-trivial number of tasks experiences severely degraded search performance after upgrading to v2.3. Search is a core workflow feature, and 10-15s latency effectively makes it unusable for interactive use.","recommended_fix":"Diff all search-related code between v2.2.x and v2.3. Profile the search query execution to identify where time is spent. Likely candidates: (1) check for dropped or missing database indexes on searchable fields, (2) review any query changes that may have removed LIMIT clauses or added unindexed WHERE conditions, (3) check if a caching layer was removed or bypassed, (4) look for N+1 query patterns introduced in the search results path.","proposed_test_case":"Add a performance benchmark test that seeds 5,000+ tasks and asserts search queries complete within 2 seconds. Run this test as part of CI to prevent future search performance regressions.","information_gaps":["Which specific search queries are slowest (keyword search, filtered search, or all searches equally)","Whether the backend shows slow query logs or if the latency is elsewhere in the stack"]}

---

