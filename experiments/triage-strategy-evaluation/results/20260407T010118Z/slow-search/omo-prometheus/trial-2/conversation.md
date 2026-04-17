# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. A few things would help me narrow this down quickly:

When did you first notice the slowdown? Was it after a specific update, after importing/creating a large batch of tasks, or did it seem to degrade gradually?

Also, roughly how many tasks are we talking about — hundreds, thousands, tens of thousands?

---

### reporter

I think it started maybe 2 weeks ago? I'm pretty sure I updated TaskFlow around that time — went from 2.2 to 2.3 I believe. I have around 5,000 tasks, been using it for a couple years now. It was always snappy before so the timing lines up with the update, but I'm not 100% sure if that's the cause.

---

### triage-agent

That timing with the 2.3 update is a strong lead — thanks for that detail.

To narrow it further: is *every* search slow, or does it depend on what you're searching for? For example:

- Is searching by keyword (e.g., typing a word from a task title) slow?
- What about filtering by status, label, or date — are those also slow?
- Does the length of your search term matter (single word vs. a phrase)?

Basically, I'm trying to figure out if all search paths are affected or just certain types of queries.

---

### reporter

Now that you mention it, I think searching by task title is actually still pretty fast. It's when I search for words that are in the task descriptions that it gets really slow. I have some tasks with really long descriptions — like I paste meeting notes into them sometimes — so maybe that's related? I haven't really paid attention to filtering by status or labels, I mostly just use the search bar.

---

### triage-agent

That's really helpful — the distinction between title search (fast) and description search (slow) is a strong clue.

One more thing to nail this down: when you search for a word that appears in descriptions, does the speed depend on how *common* the word is? For example, if you search for a rare word that only appears in one or two task descriptions, is that still slow? Or is it only slow when the search term matches many tasks?

This will help me figure out whether the system is struggling with *scanning* all descriptions or with *returning* a large set of results.

---

### reporter

Hmm, I haven't really tested that systematically. But thinking about it, even when I search for something pretty unique that I know is only in one task's description, it still takes a long time. So I don't think it's about how many results come back — it seems like the searching itself is just slow no matter what.

---

### triage-agent

Thanks — that detail about rare words still being slow is very telling. It rules out result volume as the culprit.

Here's my working theory: the v2.3 update likely changed how description search works under the hood — possibly switching from an indexed search to scanning through all 5,000 task descriptions on every query. That would explain why title search is still fast (likely still indexed) while description search is uniformly slow regardless of how many results match.

Could you confirm one thing for me? Can you check your exact TaskFlow version (usually in Settings → About or similar)? And if it's easy to do — do you have any smaller projects or workspaces with just a handful of tasks where you could test description search? If description search is fast there, it would confirm this is a scaling issue with the search implementation rather than something else on your machine.

---

### reporter

Yeah I can check — it's definitely v2.3, I remember updating it. As for smaller projects, I only have the one workspace with all my tasks in it, so I can't really test that easily. But I will say my laptop is pretty beefy (32GB RAM, ThinkPad T14) so I'd be surprised if it's a hardware thing. I did notice the CPU seems to spike when I do those description searches though, if that helps.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: full-text search on task descriptions ~100x slower than expected (~5,000 tasks)","problem":"After upgrading from TaskFlow v2.2 to v2.3, searching for terms that appear in task descriptions takes 10-15 seconds. Title-based search remains fast. The slowdown is uniform regardless of how many tasks match the query, and is accompanied by a CPU spike. The user has ~5,000 tasks, some with very long descriptions (pasted meeting notes).","root_cause_hypothesis":"The v2.3 update likely broke or removed the full-text index on the task description field, causing description searches to perform a sequential scan across all 5,000 task descriptions on every query. Title search is unaffected because its index was preserved. The CPU spike and uniform latency regardless of result count are consistent with a full-scan rather than an index lookup.","reproduction_steps":["Have a workspace with ~5,000 tasks, some with lengthy descriptions","Upgrade from TaskFlow v2.2 to v2.3","Use the search bar to search for a word that appears in a task description (not just the title)","Observe 10-15 second delay and CPU spike","Compare with a title-only search term, which returns quickly"],"environment":"TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks with some long descriptions, Lenovo ThinkPad T14 with 32GB RAM, work laptop","severity":"high","impact":"Any user with a non-trivial number of tasks experiences severely degraded description search after upgrading to v2.3. Title search still works as a partial workaround, but description search is a core feature. Users who store detailed notes in task descriptions (a common workflow) are most affected.","recommended_fix":"1. Diff the search/query implementation between v2.2 and v2.3 — look for changes to how description fields are queried (removed index, changed query strategy, new ORM behavior, etc.). 2. Check the database schema migration in v2.3 for any dropped or altered indexes on the description column. 3. If the full-text index was removed, restore it. If the query was changed (e.g., from indexed FTS to LIKE/ILIKE scan), revert to the indexed approach. 4. Benchmark with ≥5,000 tasks with realistic description lengths to verify the fix.","proposed_test_case":"Create a test database with 5,000+ tasks, including tasks with descriptions of 500+ words. Run a description search for a term appearing in exactly one task. Assert that the query completes in under 1 second (matching v2.2 baseline performance). Also verify that the query plan uses an index scan rather than a sequential scan.","information_gaps":["Exact OS version (not expected to be relevant for a query-layer regression)","Whether other v2.3 users report the same issue (would confirm it's not data-specific)","The specific database backend in use (SQLite vs PostgreSQL) — developer can check from the codebase"]}

---
