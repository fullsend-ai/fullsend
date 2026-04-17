# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down the cause, can you tell me what kind of search you're typically doing when you see the slowdown?

1. **Simple keyword search** — searching for a word in task titles
2. **Filtered search** — using filters like status, assignee, date range, etc.
3. **Full-text search** — searching across task descriptions and comments
4. **Something else**

Also, when you say it "used to be fast," do you have a rough sense of when it started slowing down — for example, after an update, or after your task count grew past a certain point?

---

### reporter

It's definitely the full-text search — when I search across task descriptions it's super slow. But if I just search by task title, it's still quick like it used to be. As for when it started, I think it was maybe 2 weeks ago? I'm pretty sure it was around when I updated to v2.3, but I'm not 100% certain on the timing.

---

### triage-agent

Thanks, that's really helpful — the fact that title search is still fast but full-text search is slow is a strong clue.

Here's what I think is happening: **v2.3 likely introduced a regression in full-text search**, possibly a change to how task descriptions and comments are indexed or queried. The title search still works on a simpler, faster code path, which is why it's unaffected.

Does that match your experience, or do you think something else might be going on?

Also, roughly how many tasks are in your workspace? A ballpark is fine:
1. Hundreds (under 1,000)
2. Low thousands (1,000–5,000)
3. Many thousands (5,000+)

---

### reporter

Yeah, that makes sense to me — it really does feel like something changed with the update. I have around 5,000 tasks, been using TaskFlow for about 2 years now. A lot of my tasks have pretty long descriptions too, like I paste meeting notes into them sometimes. Before the update, searches came back in under a second even with all that data.

---

### triage-agent

[RESOLVED] {"title":"Full-text search regression in v2.3: 10-15s response time on ~5k task workspaces","problem":"Full-text search across task descriptions and comments takes 10-15 seconds to return results, up from sub-second prior to v2.3. Title-only search remains fast. The issue affects workspaces with ~5,000 tasks, many with lengthy descriptions (e.g., pasted meeting notes).","root_cause_hypothesis":"v2.3 likely introduced a change to the full-text search indexing or query path — possibly a missing or dropped index on task descriptions/comments, a change from indexed search to sequential scan, or a regression in query construction. The fact that title search is unaffected confirms the issue is isolated to the full-text code path.","reproduction_steps":["Set up or use a workspace with ~5,000 tasks, including tasks with long descriptions","Upgrade to v2.3","Perform a full-text search (across task descriptions/comments) and measure response time","Compare with a title-only search on the same workspace to confirm the discrepancy"],"environment":"TaskFlow v2.3, ~5,000 tasks with lengthy descriptions, work laptop (OS unspecified)","severity":"high","impact":"Full-text search is effectively unusable for power users with large workspaces. These are likely the most engaged users who rely on search to navigate years of accumulated tasks.","recommended_fix":"Diff the full-text search query path between v2.2 and v2.3. Check for: dropped or rebuilt full-text indexes, changes to search query construction (e.g., switching from indexed lookup to LIKE/sequential scan), new joins or subqueries on the comments table, or removed query result caching. Run EXPLAIN/ANALYZE on the full-text search query against a ~5k task dataset to identify the bottleneck.","proposed_test_case":"Performance test: execute a full-text search on a workspace with 5,000+ tasks (including tasks with 1,000+ character descriptions) and assert response time is under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores prior performance.","information_gaps":["Exact database backend (SQLite, PostgreSQL, etc.) — though the developer will know this from the codebase","Whether the issue reproduces on a fresh v2.3 install or only on upgrades (migration-related index issue vs. code regression)"]}

---
