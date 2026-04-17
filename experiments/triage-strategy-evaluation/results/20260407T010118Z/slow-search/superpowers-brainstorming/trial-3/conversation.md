# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down what's going on, can you tell me a bit more about how you're using search?

1. **What are you typically searching for?** For example:
   - Searching by task title/keywords
   - Filtering by labels, status, or assignee
   - Full-text search across task descriptions
   - Something else?

2. **When did you first notice the slowdown?** For instance, did it start after a specific update, after your task count grew past a certain point, or seemingly at random?

---

### reporter

I mostly search across task descriptions — like searching for keywords from meeting notes or project details I've put in the description field. Searching by title actually seems fine, now that I think about it. It's the description search that's really slow.

As for when it started, I think it was maybe 2 weeks ago? I upgraded to v2.3 around that time so it might be related, but I'm not 100% sure if it was right after the upgrade or a few days later.

---

### triage-agent

[RESOLVED] {"title":"Full-text search on task descriptions regressed to ~10-15s after v2.3 upgrade","problem":"Searching across task descriptions takes 10-15 seconds, while title search remains fast. The reporter has a large number of tasks and noticed the slowdown approximately 2 weeks ago, coinciding with an upgrade to v2.3.","root_cause_hypothesis":"The v2.3 release likely introduced a regression in the description search path — either a full-text index on the description field was dropped/not migrated, or the search query was changed to do an unindexed scan (e.g., switching from indexed full-text search to a LIKE/ILIKE pattern match). The fact that title search is fast while description search is slow points to an index or query difference between these two code paths.","reproduction_steps":["Set up a TaskFlow instance on v2.3 with a large task set (reporter has 'a lot of tasks')","Perform a search by task title keyword — observe fast response","Perform a search by task description keyword — observe 10-15 second response time","Compare query plans (EXPLAIN ANALYZE or equivalent) for title search vs description search to confirm index usage"],"environment":"TaskFlow v2.3, running on a work laptop (local/desktop deployment), large task count","severity":"medium","impact":"Users with large task sets who rely on description search experience 10-15 second delays, making the feature effectively unusable for interactive workflows. Title-only search is unaffected.","recommended_fix":"1. Diff the search implementation between v2.2 and v2.3, focusing on the description search query and any migration scripts. 2. Check whether a full-text index exists on the task description column. 3. If missing, add/restore the index. If present, check whether the v2.3 query actually uses it (e.g., look for LIKE '%term%' patterns that bypass indexes). 4. Verify the fix restores sub-second search times on a representative dataset.","proposed_test_case":"Seed the database with 10,000+ tasks with varied descriptions. Search for a keyword that appears in ~50 task descriptions. Assert that results return in under 2 seconds. Run this test against both title and description search paths.","information_gaps":["Exact number of tasks in the reporter's instance (not critical — reproduce with any large dataset)","Whether the reporter is using the default database engine or a custom configuration"]}

---
