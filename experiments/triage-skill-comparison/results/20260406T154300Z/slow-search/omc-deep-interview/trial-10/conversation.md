# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down what's going on — do you remember roughly when the search started feeling slow? For example, did it happen after a specific update, or after you hit a certain number of tasks? And approximately how many tasks are we talking about in your workspace?

---

### reporter

Yeah, I think it started about 2 weeks ago. I upgraded from v2.2 to v2.3 right around that time, so it might be related to the update. I have about 5,000 tasks — I've been using TaskFlow for a couple of years now. It was always snappy before, even with all those tasks.

---

### triage-agent

That timing lines up nicely with the v2.3 upgrade — helpful to know.

One more thing that would really help us track this down: is it *every* search that's slow, or does it depend on what you're searching for? For example, does a simple one-word search like "meeting" take just as long as a more complex search with filters (status, tags, date ranges, etc.)? And does the very first search after opening TaskFlow feel any different from subsequent ones?

---

### reporter

Good question — now that I think about it, it's not every search. When I search by task title it's still pretty quick. But when I search through task descriptions, that's when it gets really slow. I do a lot of searching through descriptions because I paste meeting notes into my tasks and need to find things later. A simple one-word search in descriptions is just as slow as anything else. And it doesn't matter if it's the first search or the tenth — they're all equally sluggish when searching descriptions.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s latency on workspaces with ~5,000 tasks","problem":"After upgrading from v2.2 to v2.3, searching through task descriptions takes 10-15 seconds regardless of query complexity. Title search remains fast. The user has approximately 5,000 tasks with substantial description content (meeting notes pasted into tasks). The slowdown is consistent — not affected by cold/warm start or query length.","root_cause_hypothesis":"The v2.3 release likely introduced a change to how description search is executed — most probably a dropped or altered full-text index on the task descriptions column/field, a change from indexed search to sequential scan, or a new search implementation (e.g., switching search backends) that doesn't handle large text fields efficiently. The fact that title search is unaffected suggests titles are still indexed while descriptions are not, or that description search now takes a fundamentally different code path.","reproduction_steps":["Install TaskFlow v2.3","Create or import a workspace with ~5,000 tasks, with non-trivial description content (e.g., multi-paragraph text in descriptions)","Perform a search scoped to task descriptions using a simple one-word query","Observe response time — expected: 10-15 seconds; expected in v2.2: sub-second"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks with meeting-notes-sized descriptions","severity":"medium","impact":"Users with large workspaces who search task descriptions experience severe latency. Power users who store meeting notes in task descriptions and rely on description search are most affected. Title search remains a partial workaround but doesn't cover the primary use case.","recommended_fix":"1. Diff the search-related code and database migrations between v2.2 and v2.3 — look for changes to description indexing, query construction, or search backend. 2. Check whether a full-text index on task descriptions was dropped, altered, or failed to migrate. 3. Run EXPLAIN/query plan analysis on the description search query against a 5,000-task dataset. 4. If an index was dropped, restore it; if the search backend changed, ensure descriptions are properly indexed in the new backend.","proposed_test_case":"Performance regression test: seed a test database with 5,000 tasks containing realistic description content (200+ words each). Assert that a single-word description search completes in under 2 seconds. Run this test against both v2.2 and v2.3 search code paths to confirm the regression and verify the fix.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the v2.3 correlation)","Whether other v2.3 users with large workspaces also experience this (would confirm it's not environment-specific)","Database backend in use (SQLite vs PostgreSQL vs other) — could affect index behavior"],"clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.8,"impact":0.7,"overall_ambiguity":0.18}}

---
