# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. To help us narrow down the cause — can you recall roughly when the slowdown started? For example, did it coincide with a TaskFlow update, importing a large batch of tasks, or any other change? Also, approximately how many tasks do you have in your workspace, and does the slowdown happen with every search query or only certain ones (e.g., longer queries, specific filters)?

---

### reporter

Now that you mention it, I think the slowdown started about 2 weeks ago, right around when I upgraded from v2.2 to v2.3. I have about 5,000 tasks built up over the past couple years. And actually, good question about which searches — I just tested it and searching by task title is still snappy, but when I search across task descriptions that's when it gets really slow. Some of my tasks have pretty long descriptions (I paste meeting notes into them sometimes). Hope that helps narrow it down!

---

### triage-agent

That's really helpful — the v2.3 upgrade timing and the title-vs-description difference are great clues. One more thing that would help us zero in on the cause: when you do a description search, does the speed seem to depend on what you search for? For instance, is a single common word like "meeting" just as slow as a longer or more specific phrase? And separately, do you notice any spike in CPU or memory usage (e.g., fans spinning up, system feeling sluggish) while the search is running, or does it just seem to silently hang before returning results?

---

### reporter

Hmm, I just tried a few different searches and honestly it seems about equally slow whether I search for a common word like "meeting" or a specific phrase like "Q3 budget review" — both take around 10-15 seconds. And yeah, I do notice my laptop's fan kicks up when I search. I popped open system monitor once out of curiosity and saw one CPU core pegged at 100% during the search. It drops back down as soon as the results come back. So it's definitely doing *something* heavy, not just waiting on a network call or anything like that.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: CPU-bound full scan causes 10-15s latency at ~5k tasks","problem":"After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds and pegs a CPU core at 100%. Title search remains fast. The slowdown is consistent regardless of query complexity (single common word vs. specific phrase) and occurs on a dataset of ~5,000 tasks, many with long descriptions (pasted meeting notes).","root_cause_hypothesis":"v2.3 likely introduced a regression in the description search path — most probably removed or broke a full-text index on the description field, or switched from an indexed/optimized search to a brute-force in-memory scan. The CPU saturation (single core at 100%) and query-independent latency are consistent with a linear scan over all description text rather than an index lookup. The fact that title search is unaffected suggests the title field still has a working index while the description field does not.","reproduction_steps":["Install TaskFlow v2.3","Create or import a workspace with ~5,000 tasks, including tasks with long descriptions (multi-paragraph text)","Perform a search scoped to task descriptions using any query (e.g., 'meeting')","Observe 10-15 second delay and 100% CPU usage on one core","Compare: perform the same search scoped to task titles only — should return near-instantly","Optionally compare: downgrade to v2.2 and repeat the description search to confirm it was fast in the prior version"],"environment":"TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks with some long descriptions, running on a work laptop (OS/specs not specified but not likely relevant given the regression nature)","severity":"medium","impact":"Users with large task collections who rely on description search are significantly impacted — the feature is functionally unusable at 10-15s per query. Title search still works as a partial workaround. This is a regression from v2.2, so all users who upgraded to v2.3 with substantial task counts are likely affected.","recommended_fix":"1. Diff the search implementation between v2.2 and v2.3 to identify what changed in the description search path. 2. Check whether a full-text index on the description field was dropped, altered, or is no longer being used by the query planner. 3. If an index was removed, restore it. If the search algorithm changed, profile the new path and optimize or revert. 4. Consider adding query performance benchmarks against a dataset of ≥5k tasks with realistic description lengths to catch similar regressions in CI.","proposed_test_case":"Performance test: with a dataset of 5,000 tasks (descriptions averaging 500+ words), assert that a description search for any single-word query returns results in under 2 seconds. Run this against both v2.2 (baseline) and the fix branch to confirm the regression is resolved.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to matter given this is a regression)","Whether other v2.3 users with large datasets are also affected (likely, but unconfirmed)","The specific v2.3 changelog entry or commit that changed the search path"],"clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.85,"impact":0.75,"overall_ambiguity":0.15}}

---

