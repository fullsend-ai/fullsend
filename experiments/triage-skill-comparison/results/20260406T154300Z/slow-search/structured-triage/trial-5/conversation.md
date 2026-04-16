# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us track down the slowness, could you walk us through the steps to reproduce it? Specifically: what kind of search are you running when it's slow (e.g., a keyword search, filtering by date/tag, etc.), roughly how many tasks are in your workspace, and does it happen on every search or only with certain queries?

---

### reporter

It's specifically when I search for keywords in task descriptions — like searching for a phrase I know is buried in a task's details. I have about 5,000 tasks built up over the last couple years. It happens pretty much every time I do that kind of search. Weirdly, if I search by task title it's still snappy. It's only when it's searching through the full descriptions that it crawls.

---

### triage-agent

Thanks for those details — the distinction between title search (fast) and description search (slow) is really helpful for narrowing this down.

Could you share a few environment details? Specifically: what OS is your work laptop running (e.g., Windows 11, macOS Sonoma), which browser and version you're using TaskFlow in, and what version of TaskFlow you're on? (You can usually find the version under Settings or Help > About.)

---

### reporter

I'm on Ubuntu 22.04, it's a ThinkPad T14. I'm running TaskFlow v2.3 — I actually upgraded from v2.2 about two weeks ago, and now that I think about it, the slowness started around that same time. As for the browser, I'm not sure it's relevant since I use the desktop app, but I can check if needed.

---

### triage-agent

[RESOLVED] {"title":"Full-text search on task descriptions regressed to 10-15s after v2.3 upgrade (~5,000 tasks)","problem":"Searching by keyword within task descriptions takes 10-15 seconds to return results, whereas searching by task title remains fast. The reporter has approximately 5,000 tasks. The slowness began around the time of upgrading from TaskFlow v2.2 to v2.3.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in description search — possible causes include a dropped or changed full-text index on the task descriptions column, a query plan change (e.g., switching from indexed search to sequential scan), or a new ORM/query path that doesn't use the existing index. The fact that title search is unaffected suggests the title index is intact but the description search path diverged.","reproduction_steps":["Create or use a workspace with approximately 5,000 tasks that have text in their descriptions","Open the search feature in the TaskFlow desktop app","Search for a keyword that appears in a task's description (not just its title)","Observe that results take 10-15 seconds to return","Compare by searching for a keyword that appears in a task title — this should return quickly"],"environment":"Ubuntu 22.04, ThinkPad T14, TaskFlow v2.3 desktop app (upgraded from v2.2 approximately two weeks ago)","severity":"high","impact":"Any user with a non-trivial number of tasks experiences severely degraded search performance when searching task descriptions, a core workflow. The feature is still functional but practically unusable for power users with large task histories.","recommended_fix":"Compare the v2.2 and v2.3 description search query paths and database migrations. Check whether a full-text index on the descriptions column was dropped, altered, or is no longer being used by the query. Run EXPLAIN/ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm whether it's doing a sequential scan. Restore or add the appropriate index and verify the query planner uses it.","proposed_test_case":"Performance test: seed a database with 5,000+ tasks with varied descriptions, execute a keyword search against descriptions, and assert that results return within an acceptable threshold (e.g., under 1 second). This test should run against both title and description search to catch future regressions in either path.","information_gaps":["No error messages or logs were collected — query timing logs or database slow-query logs could confirm the exact bottleneck","Unknown whether the issue reproduces on a fresh v2.3 install or only on upgraded instances (migration-specific issue vs. code-level regression)"]}

---
