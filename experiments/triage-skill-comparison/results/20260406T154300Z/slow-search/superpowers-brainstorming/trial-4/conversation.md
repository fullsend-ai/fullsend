# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down what's going on, can you recall roughly when search started feeling slow?

1. **After a specific update** — you noticed it right after updating TaskFlow to a newer version
2. **Gradually over time** — it got slower as you added more tasks over weeks/months
3. **Suddenly, without any obvious change** — it was fine one day and slow the next
4. **Something else**

Also, roughly how many tasks are we talking about — hundreds, thousands, or tens of thousands?

---

### reporter

It was after an update — I upgraded from v2.2 to v2.3 about two weeks ago, and I'm pretty sure that's when it started. I have around 5,000 tasks built up over the last couple of years. Search used to be snappy, well under a second, but now it's painfully slow.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3: 10-15s latency with ~5,000 tasks (was <1s in v2.2)","problem":"After upgrading from TaskFlow v2.2 to v2.3, search results that previously returned in under one second now take 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over two years. No other changes were made; the regression correlates directly with the version upgrade roughly two weeks ago.","root_cause_hypothesis":"The v2.3 release likely changed the search implementation in a way that degrades performance at scale — possible causes include removal or breakage of a search index, a switch from indexed/optimized queries to full table scans, introduction of unintended eager-loading of related data, or a new feature (e.g., full-text search across additional fields) that lacks proper indexing.","reproduction_steps":["Set up a TaskFlow instance with ~5,000 tasks (or import a representative dataset)","Run a search query on v2.2 and record response time","Upgrade to v2.3 and run the same search query","Observe that response time increases from <1s to 10-15s"],"environment":"TaskFlow v2.3 (upgraded from v2.2), running on a work laptop, ~5,000 tasks","severity":"high","impact":"Search is a core workflow feature. A 10-15x slowdown affects any user with a non-trivial number of tasks, making the feature effectively unusable for daily work. All v2.3 users with moderately large task collections are likely affected.","recommended_fix":"Diff the search-related code and queries between v2.2 and v2.3. Profile the search query execution in v2.3 against a ~5,000 task dataset to identify the bottleneck (missing index, unoptimized query, excessive data loading). Check for new database migrations in v2.3 that may have dropped or failed to create indexes. Restore the performant query path or add appropriate indexing.","proposed_test_case":"Performance test: with a seeded database of 5,000 tasks, assert that search queries return results in under 2 seconds. Run this test against both v2.2 and v2.3 code paths to confirm the regression and validate the fix.","information_gaps":["Whether the slowdown affects all search queries equally or only certain query patterns (e.g., broad vs. narrow searches)","Whether other v2.3 users with large task counts are experiencing the same regression","Exact database backend in use (SQLite, PostgreSQL, etc.)"]}

---

