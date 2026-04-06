# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down what's going on — can you recall roughly when searches started feeling slow? For example, did it coincide with a TaskFlow update, a big import of tasks, or any other change you noticed? And roughly how many tasks do you have in your workspace at this point?

---

### reporter

Yeah, I think it started about 2 weeks ago. I updated from v2.2 to v2.3 right around that time, so it might be related to the upgrade. I have around 5,000 tasks — I've been using TaskFlow for about 2 years now. Before the update, search was basically instant, under a second.

---

### triage-agent

That's really helpful — the v2.3 upgrade timing is a strong lead. One more thing that would help us reproduce this: does the slowness happen on *every* search, or only certain kinds? For instance, is searching for a single common word just as slow as searching for something very specific? And does it matter whether you're searching task titles only or across descriptions/comments too (if you use those filters)?

---

### reporter

Good question — now that you mention it, searching by title is still fast, basically instant like before. It's when I search across task descriptions that it gets really slow. And it doesn't seem to matter what I search for, common or specific words, it's always slow when it's searching descriptions. I should mention some of my tasks have pretty long descriptions — I paste meeting notes into them sometimes, so some are quite lengthy.

---

### triage-agent

[RESOLVED] {"title":"Search across task descriptions regressed to 10-15s in v2.3 (was <1s in v2.2)","problem":"After upgrading from TaskFlow v2.2 to v2.3, searching across task descriptions takes 10-15 seconds regardless of query specificity. Title-only search remains fast (<1s). The user has ~5,000 tasks accumulated over 2 years, some with lengthy descriptions (pasted meeting notes). The regression appeared immediately after the v2.3 upgrade approximately 2 weeks ago.","root_cause_hypothesis":"The v2.3 release likely introduced a change to how description search is performed — most probably a missing or dropped database index on the descriptions column, a switch from indexed/FTS lookup to a full table scan, or a newly added processing step (e.g., parsing, sanitization, or ranking) that runs per-row during description search. The fact that title search is unaffected suggests the two search paths diverge and only the description path was modified.","reproduction_steps":["Install TaskFlow v2.3","Seed the workspace with ~5,000 tasks, ensuring a meaningful subset have long descriptions (>500 words, simulating pasted meeting notes)","Perform a search with scope set to include task descriptions","Observe query latency (expected: 10-15 seconds)","Compare against a title-only search on the same dataset (expected: <1 second)","Optionally repeat on v2.2 with the same dataset to confirm the regression"],"environment":"TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks with some lengthy descriptions, running on a work laptop (OS and hardware specs not specified but likely not the bottleneck given v2.2 was fast on the same machine)","severity":"medium","impact":"Any user with a substantial task count who searches across descriptions will experience multi-second delays. Power users with large workspaces (accumulated tasks, long descriptions) are most affected. Title-only search is a partial workaround but forces users to change their workflow.","recommended_fix":"1. Diff the search query path between v2.2 and v2.3, focusing on the description search branch. 2. Check for dropped or missing indexes on the task descriptions table/column. 3. Profile the description search query with EXPLAIN/ANALYZE on a dataset of ~5,000 tasks with long descriptions. 4. If an index was removed, restore it; if a new processing step was added, ensure it runs post-query rather than per-row, or add appropriate indexing (e.g., full-text search index).","proposed_test_case":"Performance regression test: seed a test database with 5,000 tasks (descriptions averaging 500+ words). Assert that a full-text search across descriptions returns results in under 2 seconds. Run this test against both v2.2 and v2.3 code paths to catch regressions.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be the root cause given the v2.2 baseline)","Whether other v2.3 users with large workspaces also experience the slowdown (likely yes, but unconfirmed)","The specific v2.3 changelog entries related to search or database schema changes"],"clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.75,"impact":0.7,"overall_ambiguity":0.175}}

---

