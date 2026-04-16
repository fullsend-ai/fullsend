# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! A couple of things would really help narrow this down: roughly when did you first notice the slowdown — days ago, weeks, after an update? And do you have a sense of how many tasks you currently have in TaskFlow? Knowing whether this crept in gradually (e.g., as your task list grew) versus appeared suddenly (e.g., after an update or migration) would point us in very different directions.

---

### reporter

It started about 2 weeks ago, pretty much right after I upgraded from v2.2 to v2.3. So it was sudden, not gradual. I've been using TaskFlow for about 2 years now and I'd estimate I have around 5,000 tasks at this point. Search was always snappy before the upgrade — under a second. Now it just hangs for 10-15 seconds.

---

### triage-agent

That's really helpful — the v2.3 upgrade timing is a strong clue. One more thing that would help a developer zero in on this: does the slowdown happen on *every* search, or only certain kinds? For example, is searching for a single common word just as slow as searching for something very specific? And does it matter whether you're searching across all 5,000 tasks or within a filtered view (e.g., a specific project or tag)? Basically trying to understand if it's "all search is broken" versus "search is slow under specific conditions."

---

### reporter

Good question — now that you mention it, I think it's specifically when I search across task descriptions that it's slow. If I search by task title it's still pretty fast, almost like before. But searching through descriptions is where it just crawls. It doesn't seem to matter what I search for — common word or specific phrase, it's equally slow. I haven't tried filtering to a specific project first, I usually just search across everything. Some of my tasks have really long descriptions too, like copy-pasted meeting notes that can be pretty lengthy.

---

### triage-agent

[RESOLVED] {"title":"Search across task descriptions regressed to 10-15s in v2.3 (title search unaffected)","problem":"After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds regardless of query term. Searching by task title remains fast (~sub-second). The reporter has approximately 5,000 tasks, many with lengthy descriptions (e.g., pasted meeting notes). Search was consistently fast prior to the upgrade.","root_cause_hypothesis":"The v2.3 release likely changed how description search is performed — most probably a missing or dropped full-text index on the task descriptions column, a switch from indexed full-text search to unindexed LIKE/ILIKE scanning, or a new query path that loads full description text into memory before filtering. The fact that title search is unaffected suggests the title field retained its index or query strategy while the description path changed.","reproduction_steps":["Install TaskFlow v2.3 (or upgrade from v2.2)","Populate the database with ~5,000 tasks, including tasks with lengthy descriptions (multi-paragraph text)","Perform a search across all tasks using the description search mode with any search term","Observe search latency of 10-15 seconds","Compare with a title-only search on the same dataset to confirm title search is still fast"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS and specs not specified but not likely relevant given the regression is version-correlated), ~5,000 tasks with some having very long descriptions","severity":"high","impact":"Any TaskFlow user with a nontrivial number of tasks who searches by description is affected. This is a core workflow regression — search is a primary feature and 10-15s latency makes description search effectively unusable. Title-only search serves as a partial workaround but does not cover the same use cases.","recommended_fix":"Diff the search query path between v2.2 and v2.3, focusing on how task descriptions are queried. Likely fixes: (1) restore or add a full-text index on the descriptions column, (2) revert any change from indexed search to sequential scan/LIKE pattern matching, (3) check if descriptions are now being fully loaded before filtering rather than filtered at the database level. Run EXPLAIN ANALYZE on the description search query against a 5,000-task dataset to confirm the query plan.","proposed_test_case":"Performance regression test: seed database with 5,000 tasks (including tasks with 1KB+ descriptions), execute a description search, and assert results return in under 2 seconds. Run this test against both the v2.2 and v2.3 query paths to verify the regression and confirm the fix.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to matter given version-correlated regression)","Whether the reporter is using a local database or a hosted/cloud backend","Specific database engine in use (SQLite, PostgreSQL, etc.) — relevant for index implementation details","v2.3 changelog or migration scripts that may reveal the specific change"],"clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.8,"impact":0.7,"overall_ambiguity":0.18}}

---
