# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! The slowness you're describing definitely sounds frustrating.

To help us track this down, could you walk us through the exact steps to reproduce the slow search? Specifically:
- What search term or type of query are you running when it's slow?
- Roughly how many tasks do you have in your account?
- Are you searching across all tasks, or within a specific project/filter?

This will help us narrow down whether the issue is related to the query type, dataset size, or something else.

---

### reporter

I have around 5,000 tasks — I've been using TaskFlow for about 2 years so they've built up. I'm usually searching across all my tasks, not filtering by project or anything. As for the search term, it's usually just a keyword I'm looking for in my task descriptions. Like I'll search for "quarterly" or "budget" to find tasks where I mentioned those things in the description. I think searching by title might actually still be fast now that I think about it — it's when it searches through the descriptions that it gets really slow.

---

### triage-agent

Thanks for those details — that's really helpful! The distinction between title search (fast) and description search (slow) is a great clue for our developers.

Could you share a few environment details so we can try to reproduce this on our end?
- What **operating system** is your work laptop running (e.g., Windows 11, macOS Sonoma)?
- Are you using TaskFlow in a **browser** (and if so, which one) or the **desktop app**?
- What **version of TaskFlow** are you on? (You can usually find this under Settings → About or Help → About.)

---

### reporter

I'm on Ubuntu 22.04, it's a ThinkPad T14 with 32GB of RAM. I'm using the desktop app. Version is 2.3 — I actually upgraded from 2.2 maybe two weeks ago, and now that you mention it, I think the slowness might have started around that time? Not 100% sure though.

---

### triage-agent

[RESOLVED] {"title":"Full-text search on task descriptions extremely slow (~10-15s) with large dataset since v2.3","problem":"Searching across task descriptions takes 10-15 seconds for a user with ~5,000 tasks. Title-only searches remain fast. The regression likely began around the upgrade from v2.2 to v2.3 approximately two weeks ago.","root_cause_hypothesis":"The v2.3 upgrade likely changed how description search is performed — possibly a missing or dropped full-text index on the task descriptions table/field, a change from indexed search to unoptimized LIKE/sequential scan, or a regression in the search query path that bypasses indexing for description fields while title search still uses its index.","reproduction_steps":["Create or use an account with approximately 5,000 tasks with populated descriptions","Perform a search for a common keyword (e.g., 'quarterly' or 'budget') across all tasks (no project filter)","Observe that results take 10-15 seconds to return","Compare with a title-only search for the same keyword — this should return quickly"],"environment":"Ubuntu 22.04, ThinkPad T14 (32GB RAM), TaskFlow desktop app v2.3 (upgraded from v2.2 ~2 weeks ago)","severity":"medium","impact":"Users with large task histories experience significant delays when searching task descriptions, degrading core search usability. Title search is unaffected. Likely affects all users with substantial task counts on v2.3.","recommended_fix":"Diff the search query path between v2.2 and v2.3, focusing on how description search is executed. Check whether a full-text index on task descriptions was dropped or is no longer being used. Profile the description search query against a 5,000-task dataset to confirm whether it's doing a sequential scan. Restore or add proper indexing for description search.","proposed_test_case":"Performance test: with a dataset of 5,000+ tasks with populated descriptions, assert that a keyword search across descriptions returns results in under 2 seconds. Include a regression test comparing title-search and description-search query plans to ensure both use indexed lookups.","information_gaps":["Exact timing of when the slowness started relative to the v2.3 upgrade (reporter is not 100% certain)","Whether other users on v2.3 with large datasets experience the same slowness","Server-side vs client-side bottleneck (no network/performance logs available)"]}

---
