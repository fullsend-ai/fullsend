# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — slow search is definitely frustrating.

A few things that would help me narrow this down:

1. **When did you first notice the slowdown?** Was it after a TaskFlow update, or did it degrade gradually over time?
2. **How many tasks are we talking about?** Roughly — hundreds, thousands, tens of thousands?
3. **Is it all searches that are slow, or only certain queries?** For example, is searching by title slow but filtering by status fast, or is everything equally affected?

Knowing whether this is a sudden regression vs. gradual degradation will significantly change where we look first.

---

### reporter

1. It started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3. It was basically instant — one day searches were fast, the next they weren't.

2. I've got around 5,000 tasks built up over about 2 years of using TaskFlow.

3. Good question actually — now that I think about it, searching by task title is still pretty fast. It's when I search across task descriptions that it crawls. Some of my tasks have really long descriptions (I paste meeting notes into them sometimes), so maybe that's related? But it used to handle those just fine before the upgrade.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s latency on full-text description queries with large task corpus","problem":"After upgrading from TaskFlow v2.2 to v2.3, searching across task descriptions takes 10-15 seconds. Title-based search remains fast. The user has ~5,000 tasks accumulated over 2 years, some containing lengthy descriptions (pasted meeting notes). The slowdown appeared immediately after the upgrade, not gradually.","root_cause_hypothesis":"The v2.3 release likely changed the description search implementation — most probably a dropped or broken full-text index on the task descriptions column, a switch from indexed search to naive full-text scan, or a change in the search query construction (e.g., moving from a database-level LIKE/FTS query to an application-level string search). The fact that title search is unaffected while description search is slow points specifically to the description field's search path, not a general performance regression.","reproduction_steps":["Have a TaskFlow instance running v2.3 with a substantial number of tasks (~5,000) where some tasks contain long descriptions (several paragraphs or more)","Perform a search that targets task descriptions (not just titles)","Observe that results take 10-15 seconds to return","Compare: perform a title-only search and confirm it returns quickly","Optionally: downgrade to v2.2 on the same dataset and confirm description search is fast"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks with some containing long-form descriptions (pasted meeting notes)","severity":"high","impact":"Any user with a non-trivial number of tasks who searches across descriptions will experience 10-15 second delays. This effectively makes description search unusable for power users. Title search is unaffected, so there is a partial workaround.","recommended_fix":"1. Diff the search-related code between v2.2 and v2.3 — look for changes to how description search queries are constructed or executed. 2. Check whether a full-text index on the descriptions column/field was dropped, altered, or is no longer being used by the query planner. 3. Profile the description search query on a dataset with ~5,000 tasks to confirm the bottleneck. 4. If a migration in v2.3 dropped or failed to create the index, add a corrective migration. 5. Consider adding query EXPLAIN/ANALYZE logging to catch this class of regression in CI.","proposed_test_case":"Create a test fixture with 5,000 tasks, 10% of which have descriptions longer than 2,000 characters. Run a description search query and assert it completes in under 1 second. Run this test against both the v2.2 and v2.3 search code paths to confirm the regression and validate the fix.","information_gaps":["Exact OS and hardware specs of the work laptop (unlikely to be root cause given the regression timing)","Whether other v2.3 users with large task counts have reported similar issues","The specific search backend in use (SQLite FTS, PostgreSQL full-text, Elasticsearch, etc.) — the fix approach may vary"]}

---
