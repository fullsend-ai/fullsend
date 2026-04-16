# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow things down — when you search, what are you typically searching for?

1. **Full-text search** across all task content (titles, descriptions, comments)
2. **Filtering/sorting** tasks by status, label, or assignee
3. **A specific search bar** (e.g., the global search in the top nav)
4. **Something else**

Also, did the slowdown start suddenly (e.g., after an update or a specific date), or has it been gradually getting worse?

---

### reporter

It's the full-text search, option 1 — specifically when I search across task descriptions. Searching by title still seems pretty fast actually, it's the description search that crawls.

As for timing, I think it started maybe 2 weeks ago? I upgraded from v2.2 to v2.3 around that time, so it might be related to that, but I'm not 100% sure if the timing lines up exactly.

---

### triage-agent

That's really helpful — the description-only slowdown plus the v2.3 upgrade timing is a strong signal.

One more thing that'll help pin this down: roughly how many tasks are in your TaskFlow instance?

1. **Hundreds** (under ~500)
2. **Low thousands** (~1,000–5,000)
3. **Tens of thousands** (10,000+)
4. **Not sure**, but I've been using it for a while and never cleaned up old tasks

---

### reporter

Option 2 — I'd say around 5,000 give or take. I've been using TaskFlow for about 2 years and I never really delete old tasks. Some of them have pretty lengthy descriptions too, like I'll paste in meeting notes and stuff.

---

### triage-agent

[RESOLVED] {"title":"Full-text search on task descriptions regressed to 10-15s after v2.3 upgrade","problem":"After upgrading from TaskFlow v2.2 to v2.3 approximately two weeks ago, full-text search across task descriptions takes 10-15 seconds to return results. Search by title remains fast. The user has ~5,000 tasks accumulated over 2 years, some with lengthy descriptions containing pasted meeting notes.","root_cause_hypothesis":"The v2.3 release likely introduced a regression in the full-text search indexing or query strategy for task descriptions. Possible causes: (1) an FTS index on the description column was dropped or not migrated, (2) the search query was changed from an indexed full-text search to an unindexed pattern match (LIKE/ILIKE), or (3) a new search feature in v2.3 added unindexed fields to the search scope. The fact that title search remains fast while description search is slow points directly at description-specific indexing — titles are short enough to scan quickly even without an index, but large descriptions (~5K rows with lengthy text) would be severely impacted by a missing or broken FTS index.","reproduction_steps":["Set up a TaskFlow v2.3 instance with ~5,000 tasks, some with lengthy descriptions (paragraph-length or longer)","Perform a full-text search using the global search targeting task descriptions","Observe response time of 10-15 seconds","Compare with the same search on a v2.2 instance with equivalent data — expect significantly faster results","Additionally, compare description search time vs title-only search time on v2.3 to confirm the discrepancy"],"environment":"TaskFlow v2.3 (upgraded from v2.2), running on a work laptop, ~5,000 tasks with some containing lengthy descriptions (pasted meeting notes)","severity":"medium","impact":"Users with moderate-to-large task databases experience unacceptable search latency (10-15s) on description searches, making the full-text search feature effectively unusable for daily workflows. Title search is unaffected, so users are not fully blocked but lose access to a key capability.","recommended_fix":"1. Diff the v2.2→v2.3 changes to search-related code (query construction, indexing, migrations). 2. Check whether the description FTS index exists in a v2.3 database — run EXPLAIN/EXPLAIN ANALYZE on the description search query to confirm whether it's doing a sequential scan. 3. If an index was dropped, add a migration to restore it. If the query strategy changed, revert or optimize the description search path. 4. Benchmark with a dataset of ~5K tasks with large descriptions to verify the fix brings response time back to sub-second.","proposed_test_case":"Create a performance regression test that seeds a database with 5,000 tasks (with realistic-length descriptions averaging 500+ words) and asserts that a full-text description search completes within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 schemas to catch indexing regressions.","information_gaps":["Exact database backend in use (SQLite, PostgreSQL, etc.) — though the investigation path is the same regardless","Whether the user is running self-hosted or cloud-hosted TaskFlow","Whether the v2.3 upgrade ran all migrations successfully"]}

---
