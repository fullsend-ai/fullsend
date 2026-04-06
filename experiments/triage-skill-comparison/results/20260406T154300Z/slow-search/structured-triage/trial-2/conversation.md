# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us investigate, could you clarify what the expected behavior was before the slowdown? Specifically, roughly how fast were search results returning before (e.g., under a second, a couple of seconds), and approximately when did you first notice the change?

---

### reporter

Yeah, searches used to come back in under a second — basically instant. I first noticed it slowing down about 2 weeks ago. I think that lines up with when I upgraded to v2.3, but I'm not 100% sure if that's related or just a coincidence.

---

### triage-agent

Thanks for that context — the timing lining up with the v2.3 upgrade is a useful clue.

Could you walk me through the steps to reproduce the slow search? Specifically:
- What are you typing into the search bar (a single word, a phrase, a filter)?
- Roughly how many tasks do you have in TaskFlow?
- Does every search take 10-15 seconds, or only certain queries (e.g., longer search terms, or searches in a specific project)?

---

### reporter

I usually search for keywords or short phrases — like searching for 'budget review' or 'quarterly planning' to find specific tasks. I have about 5,000 tasks built up over the last couple years. It seems like every search is slow, not just certain queries. Actually, now that I think about it, I just tested something — when I search by task title it's still pretty fast. It's when I'm searching through task descriptions that it grinds to a halt. A lot of my tasks have really long descriptions since I paste meeting notes into them. Could that be related?

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3: description search takes 10-15s with large task corpus","problem":"Full-text search over task descriptions became extremely slow (~10-15 seconds) after upgrading to v2.3. Title-only searches remain fast. The user has approximately 5,000 tasks, many with lengthy descriptions (pasted meeting notes). Previously, all searches returned in under a second.","root_cause_hypothesis":"The v2.3 upgrade likely changed or broke the indexing strategy for task description fields. Possible causes: (1) a missing or dropped full-text index on the description column, (2) a switch from indexed search to naive LIKE/substring scanning, or (3) a new search codepath that doesn't use the index. The fact that title search remains fast while description search is slow strongly suggests the description field is being scanned without an index.","reproduction_steps":["Create or use an account with a large number of tasks (~5,000) where many tasks have lengthy descriptions","Perform a search using a keyword or short phrase (e.g., 'budget review' or 'quarterly planning')","Observe that the search takes 10-15 seconds to return results","Compare by searching with title-only search (if available) — this should return quickly, confirming the issue is isolated to description search"],"environment":"TaskFlow v2.3, work laptop (OS and browser not specified but likely irrelevant given this is a search/indexing issue)","severity":"high","impact":"Any user with a significant number of tasks is affected when searching by description content. This degrades a core workflow — finding tasks by keyword — from sub-second to 10-15 seconds, making the feature effectively unusable for power users with large task histories.","recommended_fix":"1. Check the v2.3 migration scripts for changes to the search index on the task descriptions table. 2. Verify that a full-text index exists on the description column in the current schema. 3. Run EXPLAIN/ANALYZE on the description search query to confirm whether it's doing a sequential scan. 4. If the index was dropped or altered, restore it. If the search codepath changed, ensure it uses the indexed query. 5. Consider adding pagination or query-time limits for large result sets.","proposed_test_case":"Create a test fixture with 5,000+ tasks where at least 500 have descriptions exceeding 1,000 characters. Execute a description search for a keyword present in ~10 tasks. Assert that results return in under 2 seconds. Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.","information_gaps":["Exact OS and browser (unlikely to be relevant)","Whether the user performed a clean upgrade or migration to v2.3","Server-side logs or query execution plans","Whether other users on v2.3 with large task counts experience the same issue"]}

---

