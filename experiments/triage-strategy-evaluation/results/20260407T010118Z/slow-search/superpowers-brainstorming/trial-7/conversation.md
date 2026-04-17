# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help narrow down where the slowdown is coming from — when you search, are you:

1. **Searching by task title/name** (e.g., typing a keyword into the search bar)
2. **Using filters** (e.g., filtering by status, assignee, date range, tags)
3. **Doing a full-text search** across task descriptions/comments
4. **Something else**

Also, you mentioned it "used to be fast" — do you recall roughly when it started slowing down (e.g., after an update, after your task list grew past a certain size)?

---

### reporter

It's mainly option 3 — full-text search across task descriptions. I search through the description content a lot because I paste meeting notes and stuff into my tasks. Searching by title actually still seems pretty fast now that I think about it.

As for when it started, I think it was maybe 2 weeks ago? I updated to v2.3 around that time, so it might be related to that, but I'm not 100% sure.

---

### triage-agent

[RESOLVED] {"title":"Full-text search across task descriptions regressed to 10-15s response times (v2.3)","problem":"Full-text search over task description content takes 10-15 seconds to return results. Title-based search remains fast. The regression appeared approximately 2 weeks ago, correlating with the upgrade to v2.3. The reporter has a large number of tasks with substantial description content (meeting notes pasted in).","root_cause_hypothesis":"The v2.3 release likely introduced a change to the full-text search path — possibilities include a dropped or misconfigured full-text index on the description/comments fields, a switch from indexed search to sequential scan, or a change in the search query logic that bypasses existing indexes. The fact that title search is unaffected suggests the title field still has a working index while the description/body search path was altered.","reproduction_steps":["Set up a TaskFlow instance on v2.3 with a non-trivial number of tasks containing substantial description text","Perform a full-text search using a keyword that appears in task descriptions","Observe response time (expected: 10-15 seconds)","Perform the same search by title — observe it completes quickly","Optionally downgrade to v2.2 and repeat to confirm the regression"],"environment":"TaskFlow v2.3, work laptop (specific OS/specs not provided but not needed to reproduce — this appears to be a server-side/application-level regression, not hardware-dependent)","severity":"high","impact":"Any user performing full-text search across task descriptions experiences 10-15 second delays. Users who rely heavily on description search (e.g., storing meeting notes in tasks) are most affected. Title search users are unaffected.","recommended_fix":"Diff the search-related code between v2.2 and v2.3. Specifically check: (1) database migration scripts for any index changes on description/body/comments columns, (2) search query construction — look for changes that might cause a sequential scan instead of an index lookup, (3) any search backend changes (e.g., switching from an FTS engine to LIKE queries). Profile the full-text search query with EXPLAIN/ANALYZE to confirm whether an index is being used.","proposed_test_case":"Create a performance regression test that seeds the database with N tasks containing realistic description content, executes a full-text search, and asserts the response time is under an acceptable threshold (e.g., 1 second). Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact number of tasks in the reporter's workspace (helps calibrate performance expectations but doesn't change investigation approach)","Whether the issue is present on a fresh v2.3 install or only on upgraded instances (could indicate a migration issue vs. code issue)"]}

---
