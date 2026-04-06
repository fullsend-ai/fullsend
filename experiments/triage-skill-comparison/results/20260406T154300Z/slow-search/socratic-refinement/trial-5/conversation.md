# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the timeline a bit better. You mentioned search used to be fast — can you recall roughly when you started noticing the slowdown? And did anything change around that time — like a TaskFlow update, a big import of tasks, a change in how you're using search, or anything else that stands out?

---

### reporter

Now that you mention it, I think it started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3. I didn't connect the two at first but the timing lines up. I didn't do any big import or anything like that — I've just been steadily adding tasks over the past couple years, I have around 5,000 of them now.

---

### triage-agent

That timing with the v2.3 upgrade is really helpful — that's a strong clue. I'd like to understand the search behavior a bit more before handing this off. When you search, is it slow every single time regardless of what you type, or have you noticed it depends on what you're searching for? For instance, is searching for a single common word just as slow as searching for something very specific? And does the first search after opening TaskFlow feel any different from subsequent ones?

---

### reporter

Hmm, good question. I just tested it a bit and actually I notice something interesting — when I search by task title it's still pretty quick, but when I search across task descriptions that's when it gets really slow. It doesn't seem to matter what words I type, any description search takes forever. And yeah, it's slow every time, first search or tenth search. No difference there. I should mention some of my tasks have really long descriptions — I paste meeting notes into them sometimes, so some are pretty lengthy.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s query times on description field while title search remains fast","problem":"After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds per query. Title-based search remains fast. The slowdown is consistent regardless of search terms, query frequency, or session duration. The user has approximately 5,000 tasks, some with very long descriptions (pasted meeting notes).","root_cause_hypothesis":"The v2.3 release likely introduced a regression in description search — most probably a dropped or ineffective full-text index on the task description column, a change from indexed search to unoptimized full-text scanning (e.g., LIKE '%term%' instead of a full-text index query), or a new code path that loads full description text into memory before filtering. The fact that title search is unaffected suggests the title field's search path was not changed.","reproduction_steps":["Install TaskFlow v2.3","Have or import a dataset of ~5,000 tasks, with some tasks containing long descriptions (several paragraphs)","Perform a search using the description search mode with any search term","Observe query time of 10-15 seconds","Switch to title search mode with the same term and observe fast results for comparison"],"environment":"TaskFlow v2.3, upgraded from v2.2. Work laptop (OS and specs not specified). ~5,000 tasks, some with lengthy descriptions containing pasted meeting notes.","severity":"high","impact":"Any user with a non-trivial number of tasks experiences unusable search performance on description searches — a core workflow. This is a regression from v2.2 where search was fast.","recommended_fix":"Diff the search implementation between v2.2 and v2.3, focusing on how description search queries are constructed and executed. Specifically check: (1) whether a full-text index on the description column was dropped or altered in a v2.3 migration, (2) whether the query plan for description search changed (run EXPLAIN/ANALYZE on the search query), (3) whether v2.3 introduced a new code path that performs in-memory filtering instead of database-level search. Restore or add proper full-text indexing on the description field and ensure the search query uses it.","proposed_test_case":"Create a performance test that populates a database with 5,000+ tasks (some with descriptions over 2,000 characters), runs a description search, and asserts the query completes in under 1 second. Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given it's a regression)","Whether the database backend is SQLite, PostgreSQL, or another engine (affects index implementation details)","Whether other v2.3 users with large task counts experience the same issue"]}

---

