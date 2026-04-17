# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down what changed, can you recall roughly when search started feeling slow?

1. After a recent TaskFlow update
2. It got gradually slower as I added more tasks
3. It started suddenly, but I'm not sure what changed
4. Something else

This will help us figure out whether we're looking at a regression in code, a scaling issue, or something environment-specific.

---

### reporter

It was after a recent update — I upgraded from v2.2 to v2.3 about two weeks ago and I'm pretty sure that's when it started. Search was snappy before that, even with all my tasks.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3 (10-15s response times, was instant in v2.2)","problem":"After upgrading from TaskFlow v2.2 to v2.3, the search feature takes 10-15 seconds to return results. Search was responsive ('snappy') in v2.2 with the same dataset on the same machine.","root_cause_hypothesis":"A change to the search implementation in v2.3 introduced a performance regression — likely a missing index, removed query optimization, new unoptimized filtering/sorting step, or a switch from indexed search to full table scan. Since the dataset didn't change, this is a code-level regression, not a scaling issue.","reproduction_steps":["Install TaskFlow v2.2 and populate with a non-trivial number of tasks","Run a search query and note response time (expected: sub-second)","Upgrade to TaskFlow v2.3","Run the same search query and observe 10-15 second response time"],"environment":"Work laptop, TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago), large task dataset","severity":"high","impact":"All users with non-trivial task counts will experience unusable search latency after upgrading to v2.3. Search is a core workflow feature, so this significantly degrades daily usage.","recommended_fix":"Diff all search-related code between v2.2 and v2.3 (query construction, indexing, ORM changes, new middleware). Profile the v2.3 search query path to identify where time is spent. Common culprits: removed or changed database index, N+1 query introduction, new full-text search implementation without proper indexing, added eager-loading of associations.","proposed_test_case":"Add a performance/benchmark test that populates the database with N tasks (e.g. 1000+) and asserts that search queries complete within an acceptable threshold (e.g. under 1 second). Run this test against both v2.2 and v2.3 to confirm the regression and later verify the fix.","information_gaps":["Exact number of tasks in the reporter's dataset","Whether all search queries are slow or only certain query patterns","Database backend in use (SQLite vs PostgreSQL, etc.)","Specific v2.3 changelog entries related to search"]}

---
