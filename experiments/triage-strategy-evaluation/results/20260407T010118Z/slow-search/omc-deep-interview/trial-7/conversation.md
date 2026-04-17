# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow things down — when you say the search is slow, are you searching across all tasks, or within a specific project or filtered view? And does the slowness happen on every search, or only with certain search terms (e.g., longer queries, special characters)?

---

### reporter

I'm searching across all my tasks, not filtered to any specific project. It seems to happen pretty consistently — doesn't matter what I search for, short or long terms. Actually, now that I think about it, I think it might be slower when I'm searching through the task descriptions versus just task titles. Like if I search by title it comes back pretty quick, but searching the full text of descriptions is where it really bogs down. I have a lot of tasks, probably around 5,000 at this point, and some of them have pretty long descriptions.

---

### triage-agent

That's really helpful context — full-text search over 5,000 tasks with long descriptions definitely narrows things down.

You mentioned it *used to be* fast. Do you have a sense of when it started slowing down? For example, did it coincide with a TaskFlow update, or was it more gradual as your task count grew over time? If you remember roughly when things were still snappy (or roughly how many tasks you had at that point), that would help us figure out whether this is a scaling issue or something that changed in the application itself.

---

### reporter

Yeah, I think it started around 2 weeks ago. I'm pretty sure it was around the time I updated to v2.3 — I was on v2.2 before that. Before the update it was always snappy, like under a second. I've had a lot of tasks for a while now so I don't think it's just the number of tasks growing, since it was fine right before the update. But I'm not 100% sure the timing lines up exactly with the upgrade, it's just what I remember.

---

### triage-agent

That timing with the v2.3 update is a really useful clue — it points toward a regression rather than a pure scaling issue.

To help us reproduce this on our end: are you running TaskFlow as a desktop app or through the browser? And do you know what database setup you're using — is it the default local/SQLite storage, or are you connected to an external database? Also, is this a self-hosted install or the cloud version?

Knowing this will help our developers set up a matching environment to confirm and isolate the slowdown.

---

### reporter

It's the desktop app, and I'm pretty sure it's just using the default storage — I didn't change any database settings when I set it up. It's a self-hosted install on my work laptop, a ThinkPad T14 running Ubuntu.

---

### triage-agent

[RESOLVED] {"title":"Full-text description search regression in v2.3: 10-15s latency on ~5,000 tasks (SQLite/desktop)","problem":"After upgrading from v2.2 to v2.3 approximately two weeks ago, searching across task descriptions takes 10-15 seconds, whereas it previously completed in under a second. Title-only search remains fast. The user has approximately 5,000 tasks, many with long descriptions.","root_cause_hypothesis":"The v2.3 release likely changed the full-text search implementation — possible causes include: (1) a dropped or misconfigured FTS index on the descriptions column in SQLite, forcing a sequential scan; (2) a query change that bypasses the FTS index (e.g., switching from FTS5 MATCH to LIKE); or (3) a new search feature (e.g., ranking, highlighting) that adds per-row processing cost that scales poorly with description length and row count.","reproduction_steps":["Install TaskFlow v2.3 desktop app on Linux (Ubuntu) with default SQLite storage","Populate the database with ~5,000 tasks, including tasks with long multi-paragraph descriptions","Perform a full-text search (searching descriptions, not just titles) with any search term","Observe response time — expect 10-15 seconds","Compare: repeat the same search with title-only search and observe it returns quickly","Optionally downgrade to v2.2 and repeat to confirm the search returns in under 1 second"],"environment":"TaskFlow v2.3 desktop app, self-hosted on a ThinkPad T14 running Ubuntu, default SQLite storage, ~5,000 tasks with long descriptions","severity":"high","impact":"Any user with a large task database who relies on full-text description search experiences severe latency after upgrading to v2.3. Title-only search still works as a partial workaround, but users who need to search description content are effectively blocked.","recommended_fix":"1. Diff the v2.2 and v2.3 search query paths — check for changes to SQLite FTS index usage on the descriptions column. 2. Run EXPLAIN QUERY PLAN on the v2.3 description search query against a 5K-row database to confirm whether the FTS index is being used. 3. If the index was dropped or the query changed to bypass it, restore FTS5 indexing on descriptions. 4. If new per-row processing was added (ranking, snippet extraction), benchmark it and optimize or make it opt-in. 5. Consider adding a migration step that rebuilds the FTS index on upgrade.","proposed_test_case":"Performance regression test: populate a test database with 5,000 tasks (descriptions averaging 500+ words). Execute a full-text description search and assert the query completes in under 2 seconds. Run this test against both SQLite FTS-indexed and non-indexed configurations to catch index regressions.","information_gaps":["Exact v2.3 sub-version or build number","Whether the SQLite FTS index exists in the reporter's database (could be confirmed by developer inspection of the schema)","Whether the v2.3 upgrade migration ran successfully or logged any warnings","RAM and disk I/O characteristics of the reporter's ThinkPad (unlikely to be the bottleneck but not confirmed)"]}

---
