# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us track down the slowdown, could you walk us through the steps to reproduce it? Specifically:

- What are you searching for (e.g., a keyword, a tag, a filter combination)?
- Roughly how many tasks do you have in your account?
- Does it happen with every search query, or only certain ones?

This will help us narrow down where the bottleneck is.

---

### reporter

I have about 5,000 tasks built up over the past couple years. It happens when I search for keywords in task descriptions — like searching for a project name or a person's name that I know is mentioned in task details. But I just noticed that when I search by task title, it's still pretty fast. It's specifically the full-text search across descriptions that crawls. Every description search is slow, doesn't matter what keyword I use.

---

### triage-agent

Thanks for those details — that's really helpful context about the full-text description search being the culprit while title search stays fast.

Could you share a few environment details so we can try to reproduce this on our end?

- What OS is your work laptop running (e.g., Windows 11, macOS Sonoma, Ubuntu 22.04)?
- Which browser and version are you using?
- What version of TaskFlow are you on? (You can usually find this under Settings → About.)

---

### reporter

I'm on Ubuntu 22.04, ThinkPad T14 with 32GB RAM. TaskFlow version is 2.3 — I actually just upgraded from 2.2 about two weeks ago, and now that I think about it, the slowness started around that same time. As for browser, it's the desktop app, not a browser.

---

### triage-agent

[RESOLVED] {"title":"Full-text search across task descriptions regressed to 10-15s after upgrade to v2.3","problem":"After upgrading from TaskFlow 2.2 to 2.3, searching by keyword across task descriptions takes 10-15 seconds per query. Title-based search remains fast. The issue affects all description search queries regardless of keyword, on an account with approximately 5,000 tasks.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in the full-text search implementation for task descriptions — possibly a dropped or changed database index on the description field, a change in the search query path (e.g., switching from indexed search to unindexed LIKE/ILIKE scan), or a new search backend that lacks proper indexing for descriptions while titles retained their existing index.","reproduction_steps":["Create or use an account with approximately 5,000 tasks that have text in their descriptions","Open the search feature in the TaskFlow desktop app (v2.3)","Search for a keyword known to appear in task descriptions (e.g., a project name or person's name)","Observe that results take 10-15 seconds to return","Search for the same keyword but scoped to task titles only","Observe that title search returns results quickly"],"environment":"Ubuntu 22.04, Lenovo ThinkPad T14, 32GB RAM, TaskFlow v2.3 (desktop app), upgraded from v2.2 approximately two weeks ago","severity":"high","impact":"Any user with a significant number of tasks experiences unusable full-text search performance. This is a core workflow for finding tasks by content and represents a regression from the prior version.","recommended_fix":"Diff the search query path and database schema between v2.2 and v2.3. Check whether a full-text index on the task descriptions column was dropped, altered, or is no longer being used by the query planner. Run EXPLAIN/ANALYZE on the description search query against a dataset of 5,000+ tasks. If an index was removed, restore it; if the query path changed, revert or optimize it.","proposed_test_case":"Performance test: execute a full-text description search on a dataset of 5,000 tasks and assert that results return within an acceptable threshold (e.g., under 2 seconds). Run this test on both v2.2 and v2.3 schemas to confirm the regression and validate the fix.","information_gaps":["No application logs or query timing data from the desktop app to confirm whether the bottleneck is client-side or database-side","Exact database backend in use (SQLite, PostgreSQL, etc.) is unknown","Whether the issue also affects the web/browser version of TaskFlow is untested"]}

---

