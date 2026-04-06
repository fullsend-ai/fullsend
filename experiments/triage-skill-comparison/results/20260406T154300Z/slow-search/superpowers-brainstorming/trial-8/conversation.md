# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down the cause, can you recall roughly when the slowdown started?

a) After a recent update of TaskFlow
b) After your task list grew past a certain size (roughly how many tasks do you have?)
c) It seemed to happen gradually over time
d) It started suddenly but I'm not sure what changed

Also — is every search slow, or only certain queries (e.g., longer search terms, specific filters)?

---

### reporter

I think it was (a) — it started about 2 weeks ago, which lines up with when I upgraded from v2.2 to v2.3. I have around 5,000 tasks built up over the past couple years.

As for which searches are slow — now that you mention it, it's specifically when I search through task descriptions that it crawls. If I just search by title, it's still snappy. The description searches are the ones that take 10-15 seconds.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s latency with ~5k tasks","problem":"After upgrading from v2.2 to v2.3, searching by task description takes 10-15 seconds. Title-only searches remain fast. The user has approximately 5,000 tasks accumulated over two years.","root_cause_hypothesis":"The v2.3 release likely changed how description search is executed — possible causes include a removed or missing database index on the description column, a switch from indexed/full-text search to a naive LIKE/full-table scan, or a new search implementation that doesn't scale with corpus size. The fact that title search is unaffected suggests the two search paths diverged in v2.3.","reproduction_steps":["Set up a TaskFlow instance on v2.3 with a dataset of ~5,000 tasks that have populated description fields","Perform a search using a term that matches task descriptions","Observe query latency of 10-15 seconds","Repeat the same search scoped to title only and observe fast response","Optionally downgrade to v2.2 and confirm description search is fast again"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop, ~5,000 tasks","severity":"high","impact":"Any user with a non-trivial number of tasks experiences unusable description search performance. This is a core workflow regression since search is a primary navigation mechanism in a task management app.","recommended_fix":"Diff the search implementation between v2.2 and v2.3, focusing on the description search query path. Check for removed indexes, changes to query construction (e.g., full-text search replaced with unindexed LIKE), or new ORM/query-builder behavior. Verify that the description column has an appropriate full-text or trigram index. Add query-level performance tests for description search at scale.","proposed_test_case":"Performance test: seed a database with 5,000+ tasks with realistic descriptions, execute a description search, and assert response time is under 1 second (or an acceptable threshold). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact database engine and version in use (could affect index behavior)","Whether the v2.3 migration included any schema changes to the tasks table","Whether the slowdown scales linearly with task count or has a threshold"]}

---

