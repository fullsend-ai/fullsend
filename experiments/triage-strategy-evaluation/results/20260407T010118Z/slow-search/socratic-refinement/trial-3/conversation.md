# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the context a bit better.

You mentioned search used to be fast but now takes forever — can you help me pinpoint when you first noticed the slowdown? Was it gradual, or did it seem to happen suddenly (e.g., after an update, after importing a batch of tasks, or after some other change)? Anything you can recall about what was different before vs. now would be really helpful.

---

### reporter

I think it started maybe 2 weeks ago? I updated TaskFlow around that time — went from 2.2 to 2.3 I believe. It wasn't gradual, it was pretty much immediate after that. I didn't import anything new or do anything unusual, just the upgrade. Before that, searches were snappy, like under a second.

---

### triage-agent

That's a really useful detail — an immediate slowdown after upgrading from 2.2 to 2.3 strongly suggests something changed in that release.

To help narrow down what's going on: can you tell me more about what your searches look like when they're slow? For example, does it happen with every search regardless of what you type, or is it worse with certain kinds of queries (like searching for a single word vs. a phrase, or filtering by tags/dates)? And when the results do come back, are they what you'd expect, or have you noticed anything off about them (e.g., too many results, missing results, different ordering)?

---

### reporter

Now that you mention it, I think it's mostly when I'm searching for something in the task descriptions. Like if I search for a keyword that I know is in the description of a task, that's when it's really slow. Searching by title seems fine actually — I just tested that and it came back right away. The results do seem correct when they finally show up, I haven't noticed anything missing or weird about them. Some of my tasks have pretty long descriptions if that matters, I sometimes paste meeting notes into them.

---

### triage-agent

This is shaping up to a pretty clear picture — let me play it back and see if it matches your experience:

It sounds like searching by description became slow immediately after upgrading to v2.3, while title search stayed fast. You have tasks with lengthy descriptions (meeting notes, etc.), and the results themselves are correct, just slow to arrive. That pattern suggests something changed in how v2.3 searches description text — possibly a missing index or a switch to a different search method that doesn't handle large text fields efficiently.

Does that match your experience? And one more thing that would help a developer reproduce this: roughly how many tasks do you have in TaskFlow? Ballpark is fine — are we talking dozens, hundreds, thousands?

---

### reporter

Yeah, that summary sounds exactly right. And I have around 5,000 tasks — I've been using TaskFlow for about 2 years now so they've built up. Some of those tasks have really long descriptions, like I said I paste meeting notes in there sometimes, some of them are probably 2000+ words. I also noticed my laptop fan kicks on when I do those searches, not sure if that's relevant.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s query times on large text fields","problem":"After upgrading from TaskFlow v2.2 to v2.3, full-text search against task descriptions became extremely slow (10-15 seconds), while title search remains fast (<1 second). The results are correct but the latency is unacceptable. High CPU usage is observed during these searches.","root_cause_hypothesis":"The v2.3 release likely changed the description search implementation — most probably a dropped or missing full-text index on the description field, a switch from indexed search to sequential scan / LIKE-based matching, or a change in the search library/query that doesn't handle large text fields efficiently. The fact that title search is unaffected suggests the regression is specific to description-field indexing or query path.","reproduction_steps":["Install TaskFlow v2.3 (or upgrade from v2.2)","Populate the database with ~5,000 tasks, some with descriptions of 2,000+ words","Search for a keyword that appears in task descriptions (not just titles)","Observe query time of 10-15 seconds and elevated CPU usage","Compare: search for the same keyword by title only — should return in <1 second","Optionally: downgrade to v2.2 and repeat to confirm the regression"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (specific OS/specs unknown but not likely relevant given it's a regression), ~5,000 tasks accumulated over 2 years, some with 2,000+ word descriptions (pasted meeting notes)","severity":"high","impact":"Any user with a substantial task history who searches by description content will experience severe slowdowns. Users with long-form descriptions (meeting notes, etc.) are most affected. This is a regression from previously-working functionality in a core feature.","recommended_fix":"Diff the search-related code and database migrations between v2.2 and v2.3. Specifically check: (1) whether a full-text index on the description column was dropped or not created in a migration, (2) whether the search query changed from indexed full-text search to unindexed pattern matching (e.g., LIKE '%term%'), (3) whether a search library was swapped or its configuration changed. Run EXPLAIN/ANALYZE on the description search query to confirm whether an index is being used. Restore or add the appropriate full-text index.","proposed_test_case":"Create a performance/regression test that populates the database with 5,000+ tasks (a subset with descriptions >1,000 words), executes a description keyword search, and asserts the query completes in under 2 seconds. This test should run against new releases to catch future search regressions.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to matter given it's a regression)","Whether other users on v2.3 experience the same issue (likely yes, given it's a code/schema change)","The specific database backend in use (SQLite vs PostgreSQL, etc.) — relevant to the fix but discoverable from the codebase"]}

---
