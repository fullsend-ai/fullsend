# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down the cause, do you recall roughly when search started slowing down?

1. **After a TaskFlow update** — it was fast on one version and slow on the next
2. **Gradually over time** — it got slower as you added more tasks
3. **Suddenly, without an obvious change** — one day it was just slow
4. **Something else**

Also, roughly how many tasks are we talking about — hundreds, thousands, or tens of thousands?

---

### reporter

It was after an update — I upgraded from v2.2 to v2.3 about two weeks ago and I'm pretty sure that's when it started. Search was snappy before that. I have around 5,000 tasks, been using TaskFlow for about 2 years now.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3 (~10-15s latency with ~5,000 tasks)","problem":"After upgrading from v2.2 to v2.3, search latency increased from sub-second to 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over 2 years of use. Search was consistently fast on v2.2.","root_cause_hypothesis":"A change introduced in v2.3 degraded search performance — likely a modified query that lost an index, a switch from indexed database search to in-memory/full-scan filtering, or a newly added search feature (e.g., full-text search across more fields) that wasn't optimized for larger datasets.","reproduction_steps":["Set up a TaskFlow instance with ~5,000 tasks (or use a seed/fixture script to generate them)","Run search on v2.2 and record latency","Upgrade to v2.3 and run the same search query","Observe 10-15 second response time on v2.3"],"environment":"TaskFlow v2.3 (upgraded from v2.2), running on a work laptop, ~5,000 tasks","severity":"high","impact":"Search is a core workflow feature. A 10-15 second delay on every search makes the application feel broken for any user with a non-trivial number of tasks. Likely affects all v2.3 users at scale, not just this reporter.","recommended_fix":"Diff all search-related code between v2.2 and v2.3 (query logic, ORM calls, database migrations). Check for missing or dropped indexes, new unindexed columns added to search scope, or removal of pagination/query limits. Profile the search query with EXPLAIN/ANALYZE on a 5,000-task dataset to identify the bottleneck.","proposed_test_case":"Add a performance/benchmark test that runs a search query against a dataset of 5,000+ tasks and asserts that results are returned within an acceptable threshold (e.g., under 2 seconds). This test should run against the search path to catch future regressions.","information_gaps":["Whether the slowness affects all search queries equally or only certain search terms/filters","Whether the database backend or storage engine was also changed during the upgrade","Exact laptop specs (CPU, RAM, disk type) — unlikely to be the root cause given the version correlation but could be a contributing factor"]}

---
