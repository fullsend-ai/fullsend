# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the change you noticed. You mentioned search used to be fast — can you tell me roughly when you first started noticing the slowdown? Did anything change around that time, like a TaskFlow update, a big import of tasks, or changes to your workflow? Also, when you say you have 'a lot of tasks,' I'm curious what that looks like for you — are we talking hundreds, thousands?

---

### reporter

Yeah, I think the slowdown started maybe 2 weeks ago? I'm pretty sure I updated TaskFlow around that time — went from 2.2 to 2.3 if I remember right. I don't think I imported anything new, just my usual workflow. As for tasks, I've got around 5,000 of them built up over a couple years. I use it pretty heavily for work.

---

### triage-agent

That's really helpful — the timing lining up with the 2.2 → 2.3 update is a strong clue. I'd love to understand a bit more about what the slowdown actually looks and feels like. When you search, does it seem like the app is working hard the whole time (maybe the UI freezes or you see a spinner), or does it feel more like it's waiting on something? And is it slow for every search you do, or have you noticed it depends on what you're searching for — like short terms vs. longer ones, or searching in a specific project vs. across everything?

---

### reporter

Now that you mention it, the app does seem to be working hard — I can hear my laptop fan spin up and the UI kind of freezes for a bit. I haven't paid super close attention to what makes it faster or slower, but actually I think searching by task title seems fine? It's when I'm searching through the task descriptions that it really bogs down. I have some tasks with really long descriptions — like I paste meeting notes into them sometimes — so maybe that's related? It seems to be slow regardless of what I type, as long as it's searching descriptions.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: full-text search across task descriptions causes 10-15s UI freeze","problem":"After upgrading from TaskFlow 2.2 to 2.3, searching task descriptions takes 10-15 seconds with high CPU usage and UI freezes. Title-only search remains fast. The user has approximately 5,000 tasks, some with very long descriptions containing pasted meeting notes. The slowdown occurs regardless of the search query, as long as descriptions are being searched.","root_cause_hypothesis":"The 2.2 → 2.3 update likely introduced a regression in description search — most probably removed or broke a full-text index on the description field, or switched from indexed search to a naive in-memory or unindexed scan. The CPU-bound behavior (fan, UI freeze) suggests the search is running synchronously on the main thread, iterating over all description text without an index. Title search remaining fast supports this, as the title field likely retained its index or is small enough to scan quickly.","reproduction_steps":["Create or use a TaskFlow instance with a large number of tasks (~5,000)","Ensure some tasks have lengthy descriptions (e.g., pasted meeting notes)","Run TaskFlow v2.3","Perform a search that includes task descriptions (not title-only)","Observe 10-15 second delay with high CPU usage and UI freeze","Compare by performing a title-only search — should return quickly"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS not specified), ~5,000 tasks accumulated over 2 years, some with very long descriptions","severity":"high","impact":"Any heavy user with a large task database is affected whenever they search descriptions. The UI freeze makes the app unusable during search, disrupting workflow for power users who rely on search.","recommended_fix":"Diff the search implementation between v2.2 and v2.3, focusing on how description fields are queried. Check whether a full-text index on descriptions was dropped, altered, or bypassed. If search now runs unindexed or on the main/UI thread, restore the index and move search to a background thread or worker. Consider adding pagination or result-count limits to prevent unbounded scans.","proposed_test_case":"Performance test: with a dataset of 5,000+ tasks (including tasks with descriptions >1KB), assert that a description search completes in under 2 seconds. Regression test: verify that the search query plan uses an index on the description field rather than a sequential scan.","information_gaps":["Exact v2.3 patch version (e.g., 2.3.0 vs 2.3.1)","Operating system and hardware specs of the reporter's laptop","Whether the search is implemented client-side (SQLite/local DB) or hits a server","Database file size on disk"]}

---
