# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — a 10-15 second search is definitely not expected. To help us track this down, could you walk us through the steps to reproduce it? Specifically:

- What are you searching for when it's slow? (e.g., a keyword, a filter, a phrase)
- Roughly how many tasks do you have in your account?
- Does it happen on every search, or only with certain queries?

This will help us figure out whether it's a scaling issue, a specific query pattern, or something else.

---

### reporter

I have about 5,000 tasks built up over a couple years. It happens pretty consistently when I search for keywords — like if I search for "budget" or "quarterly review" it takes forever. Actually, now that I think about it, I think it might be slower when it's searching through the task descriptions rather than just titles? Like if I search by title it seems okay, but the full search is really slow. I'm usually just typing a word or two, nothing fancy.

---

### triage-agent

That's really helpful — the distinction between title-only and full-text search is a great clue and will definitely help us narrow this down.

Could you share a few environment details? Specifically:

- What **OS** is your work laptop running (e.g., Windows 11, macOS Sonoma, Ubuntu)?
- Which **browser** are you using (and roughly what version)?
- Do you know what **version of TaskFlow** you're on? (You can usually find this under Settings → About.)

This will help us figure out whether it's a server-side query issue or something browser/client-related.

---

### reporter

I'm on Ubuntu 22.04, it's a ThinkPad T14. I don't think it's a browser thing though since it feels like the search itself is slow, not the page. For TaskFlow version, I'm on v2.3 — I actually upgraded from 2.2 maybe two weeks ago? I think the slowness might have started around then but I'm not 100% sure on the timing.

---

### triage-agent

[RESOLVED] {"title":"Full-text search performance regression in v2.3 (~10-15s for keyword queries on large task sets)","problem":"Full-text search (searching task descriptions, not just titles) takes 10-15 seconds to return results for users with large task counts (~5,000). Title-only search remains fast. The reporter believes the slowness began around the upgrade from v2.2 to v2.3 approximately two weeks ago.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a change to the full-text search implementation — possibly a missing or dropped database index on task description fields, a change in query strategy (e.g., switching from indexed search to sequential scan), or a new search feature that scans additional fields without optimization. The fact that title search is fast while full-text search is slow points to the description-search query path specifically.","reproduction_steps":["Create or use an account with approximately 5,000 tasks with populated descriptions","Perform a full-text search (not title-only) for a common keyword like 'budget' or 'quarterly review'","Observe that results take 10-15 seconds to return","Compare with a title-only search for the same keyword — this should return quickly"],"environment":"Ubuntu 22.04, Lenovo ThinkPad T14, TaskFlow v2.3 (upgraded from v2.2 ~2 weeks ago)","severity":"medium","impact":"Users with large task histories experience unacceptable search latency on full-text queries. This degrades daily usability for power users and long-term customers who have accumulated thousands of tasks.","recommended_fix":"1. Diff the search query path between v2.2 and v2.3 for full-text/description search. 2. Check database indexes on task description columns — verify they weren't dropped or altered in v2.3 migration. 3. Run EXPLAIN ANALYZE on the full-text search query with ~5,000 tasks to identify whether it's doing a sequential scan. 4. If an index is missing, add it; if the query changed, optimize or revert the query strategy.","proposed_test_case":"Performance test: seed a database with 5,000 tasks with realistic descriptions. Run a full-text keyword search and assert results return within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact browser and version (reporter indicated the issue feels server-side, not client-side)","Whether server-side logs show slow query warnings during the slow searches","Whether other users with large task counts also experience the issue or if it's account-specific"]}

---
