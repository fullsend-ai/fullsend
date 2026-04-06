# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! You mentioned search used to be fast — do you have a sense of what changed around the time it slowed down?

1. **TaskFlow was updated** (new version deployed or installed)
2. **My task count grew significantly** (e.g., crossed into hundreds or thousands of tasks)
3. **I started searching differently** (longer queries, using filters I didn't use before, etc.)
4. **Nothing changed that I'm aware of** — it just started being slow one day
5. Something else

Also, roughly how many tasks are in your workspace? (Ballpark is fine — dozens, hundreds, thousands?)

---

### reporter

I'm pretty sure it was #1 — I upgraded from v2.2 to v2.3 about two weeks ago, and that's right around when I noticed the slowdown. I have about 5,000 tasks built up over the last couple years, but the count hasn't changed dramatically recently. The search was snappy before the upgrade.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3 (~10-15s latency with ~5,000 tasks)","problem":"After upgrading from TaskFlow v2.2 to v2.3, search queries that previously returned near-instantly now take 10-15 seconds. The reporter's workspace contains approximately 5,000 tasks accumulated over two years. Task count has not changed significantly — the slowdown correlates with the version upgrade, not data growth.","root_cause_hypothesis":"A change to the search implementation in v2.3 introduced a performance regression at scale. Likely candidates: a query that lost an index, a switch from indexed/cached search to unoptimized full-scan, addition of new search features (e.g., full-text, fuzzy matching) without corresponding indexing, or a removed/broken caching layer.","reproduction_steps":["Provision a TaskFlow instance on v2.2 and seed it with ~5,000 tasks","Run a representative search query and record response time","Upgrade the instance to v2.3 (same dataset)","Run the same search query and record response time","Expect a significant latency increase (from sub-second to 10-15 seconds)"],"environment":"TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago), ~5,000 tasks, running on a work laptop (OS and specs not specified but unlikely to be relevant given the version-correlated regression)","severity":"high","impact":"Any user with a large task history (thousands of tasks) on v2.3 will experience unusable search latency. Search is a core workflow feature, so this likely affects daily productivity for power users.","recommended_fix":"Diff all search-related code between v2.2 and v2.3 (query logic, indexing, caching). Profile the v2.3 search path against a 5,000-task dataset to identify the hot spot. Check database query plans for missing or dropped indexes. If a new search feature was added (e.g., fuzzy matching, additional fields), ensure it is backed by appropriate indexes or can be toggled off.","proposed_test_case":"Add a performance/benchmark test that runs search queries against a seeded database of 5,000+ tasks and asserts response time stays under an acceptable threshold (e.g., 1 second). This test should run in CI to catch future regressions.","information_gaps":["Exact search queries that are slow (all queries, or specific patterns/filters)","Whether the TaskFlow instance uses a local database or a remote one","Server-side logs or query profiling data from v2.3"]}

---

