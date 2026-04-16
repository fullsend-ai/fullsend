# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. A few things to help me narrow this down quickly:

When you say search "used to be fast," can you pinpoint roughly when it became slow? Did it coincide with anything — a TaskFlow update, a big import of tasks, or a change in your setup?

Also, approximately how many tasks are we talking about — hundreds, thousands, tens of thousands?

---

### reporter

I think it started getting slow maybe 2 weeks ago? I updated TaskFlow around that time — went from 2.2 to 2.3 I believe. I have around 5,000 tasks, been using it for a couple years now. It was always snappy before the update though, even with all those tasks.

---

### triage-agent

Thanks for those details — the timing with the 2.2 → 2.3 update is a strong signal.

To dig deeper: is *every* search slow, or only certain queries? For example, does searching for a single common word take the same 10-15 seconds as searching for something very specific? And is the first search after opening TaskFlow slower than subsequent ones, or is it consistently slow every time?

---

### reporter

Now that you mention it, I don't think every search is slow. When I search for something and it matches a task title, it seems to come back pretty quick. But when I'm searching for something that's buried in the task description, that's when it really crawls. It's consistently slow too — not just the first time. I do have some tasks with really long descriptions (I paste meeting notes into them sometimes) so maybe that's related?

---

### triage-agent

That's a really useful distinction — title matches being fast while description searches are slow strongly suggests the search indexing or query strategy changed in 2.3.

One more thing to help pinpoint where the bottleneck is: during those 10-15 second waits, what do you see in the UI? Specifically:

- Is there a loading spinner or progress indicator, or does the app appear frozen/unresponsive?
- If you're using the desktop app, do you notice high CPU or memory usage during the search (e.g., fan spinning up)?

This will help us determine whether the slowness is in how the search query executes or in how results are rendered.

---

### reporter

The app doesn't freeze completely — I can still see the UI, but there's a spinner that just sits there for a while. And yeah, actually I have noticed my laptop's fan kicks up during those searches. I checked the system monitor once out of curiosity and it looked like one CPU core was pegged at 100% while it was searching. So it definitely seems like it's working hard on something. Once the results come back everything goes back to normal.

---

### triage-agent

[RESOLVED] {"title":"Search regression in v2.3: full-text description search is orders of magnitude slower than in v2.2","problem":"After upgrading from TaskFlow 2.2 to 2.3, searches that match against task description content take 10-15 seconds to return results, while title-only matches remain fast. This affects users with large task corpuses (~5,000 tasks) and long description fields. The slowdown is consistent across all description searches, not just cold starts.","root_cause_hypothesis":"The v2.3 release likely changed the search query strategy or indexing for task descriptions. The most probable cause is that full-text indexing on the description field was dropped, altered, or is not being rebuilt after migration — forcing the search to do a sequential scan/regex match across all description text. The single-core CPU saturation at 100% during searches is consistent with synchronous, unindexed text scanning rather than an indexed lookup.","reproduction_steps":["Install or upgrade to TaskFlow v2.3","Have a dataset of ~5,000 tasks, some with long description fields (e.g., pasted meeting notes)","Search for a term that appears only in task descriptions, not titles","Observe 10-15 second delay with CPU spike on one core","Compare: search for a term that matches a task title — this returns quickly"],"environment":"TaskFlow v2.3 (upgraded from v2.2), desktop app on a work laptop. Dataset: ~5,000 tasks accumulated over ~2 years, some with long descriptions.","severity":"high","impact":"Any user with a non-trivial number of tasks who searches by description content is affected. The feature is functionally degraded — 10-15 second waits on a previously instant operation disrupts workflow. Title-only search works as a partial workaround but limits search utility. Likely affects all v2.3 users proportional to their task count and description length.","recommended_fix":"Diff the search implementation between v2.2 and v2.3, focusing on how description fields are queried. Specifically investigate: (1) whether a full-text index on the description column was dropped or not created during the 2.2→2.3 migration, (2) whether the query planner is falling back to a sequential scan (check EXPLAIN output if SQL-backed), (3) whether a search library upgrade in 2.3 changed tokenization or index format without triggering a re-index. Fix should restore indexed full-text search on descriptions and include a migration path for users who already upgraded to 2.3.","proposed_test_case":"Create a test dataset with 5,000+ tasks where at least 100 tasks have description fields >500 words. Search for a term that appears only in descriptions. Assert that search completes in under 1 second (matching v2.2 baseline performance). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact operating system and hardware specs (unlikely to affect root cause but may influence performance thresholds)","Whether the user ran any migration scripts during the upgrade or if it was automatic","Whether the search backend is SQLite FTS, a dedicated search library, or custom implementation (developer will know this from the codebase)"]}

---
