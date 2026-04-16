# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. A few things to help me narrow down the issue:

When did you first notice the slowdown? Can you tie it to a specific update, or did it get gradually worse over time?

Also, roughly how many tasks are in your workspace right now?

---

### reporter

I think it started maybe 2 weeks ago? I recently updated to the newest version — I believe it was v2.3 — so it might be related to that, but I'm not 100% sure on the timing. As for tasks, I've been using TaskFlow for about 2 years now so I've got a lot in there... probably around 5,000 or so.

---

### triage-agent

Thanks for those details — v2.3 with ~5,000 tasks is helpful context.

I want to understand the pattern of the slowness more precisely: is **every** search taking 10-15 seconds, or is it specifically the first search after you open TaskFlow (with subsequent ones being faster)? And does it happen with all search terms, or are certain queries noticeably worse — for example, short/common terms vs. longer, more specific ones?

---

### reporter

It's not just the first search — it's pretty much every time. But now that you mention it, I think it might depend on what I'm searching. When I search for something in the task title it seems okay, but when I'm searching through the full descriptions it gets really slow. A lot of my tasks have pretty long descriptions — I tend to paste meeting notes and stuff in there. I hadn't really connected that until you asked.

---

### triage-agent

That's a really useful observation — it sounds like the slowness is specifically tied to searching through task descriptions, not titles. That narrows things down a lot.

Here's what I'm thinking: v2.3 may have changed how description search works under the hood — possibly dropping an index or switching to a slower search method. To test that theory:

Could you try two description searches and roughly time them? First, search for a very common word that would appear in many of your task descriptions (like "meeting" or "update"). Then search for something rare or unique that only one or two tasks would contain. Is the common-word search noticeably slower than the rare-word search, or are they both equally slow?

---

### reporter

Hmm, I just tried that. Both searches seem about equally slow honestly — whether I search for something common like "meeting" or something really specific, it still takes roughly the same amount of time when searching descriptions. Maybe the specific one was a tiny bit faster but it's hard to tell, they're both in that 10-15 second range. Title searches are still snappy though.

---

### triage-agent

[RESOLVED] {"title":"Description search performs full scan after v2.3 upgrade (~10-15s with 5k tasks)","problem":"After upgrading to approximately v2.3, searching task descriptions takes 10-15 seconds regardless of search term specificity. Title searches remain fast. The reporter has ~5,000 tasks with long descriptions (often containing pasted meeting notes).","root_cause_hypothesis":"v2.3 likely broke or removed the full-text index on the task descriptions column, or switched the description search code path from an indexed query (e.g., full-text search) to an unindexed sequential scan (e.g., SQL LIKE '%term%' or in-memory string matching). Evidence: (1) title search is still fast, suggesting title indexing is intact; (2) search time is constant regardless of how many results match, which rules out result-rendering overhead and points to a fixed-cost full-table scan; (3) timing correlates with the v2.3 upgrade.","reproduction_steps":["Have a workspace with ~5,000 tasks, many with long descriptions","Upgrade to v2.3","Perform a search that targets task descriptions (not just titles)","Observe 10-15 second response time regardless of search term commonality","Compare with a title-only search, which should return quickly"],"environment":"TaskFlow v2.3 (approximately), work laptop, ~5,000 tasks accumulated over 2 years, many with long descriptions containing pasted meeting notes","severity":"high","impact":"Any user with a large task count who relies on description search is affected. The feature is effectively unusable for interactive use. Workaround exists (search by title only) but limits functionality significantly for users who store detailed information in descriptions.","recommended_fix":"Diff the search query path between v2.2 and v2.3 for description search specifically. Check whether a full-text index on the descriptions column was dropped or a migration failed to create one. If the search method was changed (e.g., from full-text search to LIKE/regex), restore indexed search. If using a database, run EXPLAIN on the description search query to confirm a sequential scan. Also verify the index exists and is not corrupted for large text fields.","proposed_test_case":"With a dataset of 5,000+ tasks with non-trivial descriptions, assert that a description search completes in under 2 seconds for both common and rare terms. Include a regression test that verifies the query plan uses an index (not a sequential scan) for the description search path.","information_gaps":["Exact v2.3 version number not confirmed (reporter said 'I believe')","Database backend type (SQLite, PostgreSQL, etc.) not known","Whether any other operations (filtering, sorting) are also slower post-upgrade"]}

---
