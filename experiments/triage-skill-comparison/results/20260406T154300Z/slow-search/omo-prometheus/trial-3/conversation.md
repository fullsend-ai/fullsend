# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. A few things would help me narrow this down quickly:

When did you first notice the slowdown? Did it coincide with anything specific — a TaskFlow update, a big import of tasks, or a change on your laptop? Also, roughly how many tasks are we talking about (hundreds, thousands, tens of thousands)?

Finally, is it *every* search that's slow, or only certain queries (e.g., longer search terms, searching within a specific project, using filters)?

---

### reporter

It started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3. I have about 5,000 tasks — I've been using TaskFlow for a couple years now. As for which searches are slow, I just realized it's mainly when I'm searching through task descriptions. If I search by title it's still pretty fast. The description searches are the ones that take forever.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s response time with ~5,000 tasks","problem":"After upgrading from v2.2 to v2.3, searching by task description takes 10-15 seconds. Title-based search remains fast. The user has approximately 5,000 tasks accumulated over two years of use.","root_cause_hypothesis":"The v2.3 release likely introduced a regression in the description search path — most probably a missing or broken database index on the task descriptions column, a change from indexed/full-text search to a naive LIKE/sequential scan, or a new processing step (e.g., snippet generation, highlighting) that scales poorly with description length and task count.","reproduction_steps":["Set up a TaskFlow instance running v2.3","Populate with ~5,000 tasks that have non-trivial descriptions","Perform a search using a term that matches task descriptions","Observe response time of 10-15 seconds","Perform the same search scoped to titles only — observe fast response","Repeat on v2.2 with the same dataset to confirm regression"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks","severity":"medium","impact":"Users with large task databases experience unusable description search times after upgrading to v2.3. Title search is unaffected, providing a partial workaround. Likely affects any user with a non-trivial number of tasks.","recommended_fix":"Diff the search implementation between v2.2 and v2.3, specifically the query path for description search. Check for: (1) missing or dropped database index on description fields, (2) changes from full-text search to unindexed LIKE/ILIKE queries, (3) newly added per-row processing (regex, snippet extraction) that doesn't scale. Run EXPLAIN/EXPLAIN ANALYZE on the description search query against a 5,000-task dataset to identify the bottleneck.","proposed_test_case":"Performance test: seed database with 5,000+ tasks with realistic descriptions. Assert that a description search query returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores prior performance.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the version correlation)","Whether the search is backed by a SQL database, full-text engine, or in-memory — affects fix approach","Whether other v2.3 users with large datasets report the same issue"]}

---
