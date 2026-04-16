# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! The slowdown you're describing does sound excessive.

To help us investigate, could you walk us through the steps to reproduce the issue? Specifically:
- What are you searching for (e.g., a keyword, a tag, a filter combination)?
- Where are you triggering the search (the main search bar, a filtered view, etc.)?
- Does every search take 10-15 seconds, or only certain types of queries?

That will help us narrow down where the bottleneck is.

---

### reporter

I'm searching for keywords in the main search bar — just regular words that I know are in my task descriptions. Like if I search for 'budget' or 'quarterly review', it takes 10-15 seconds to come back with results. But here's the weird thing — if I search for something that's in a task title, it's almost instant. It's only slow when the match is in the description body of the task. Every time I search for something in descriptions, it's consistently slow.

---

### triage-agent

Thanks for those details — that's a really useful observation about title vs. description searches behaving differently.

Could you share a few environment details so we can try to reproduce this on our end?

- What OS and browser are you using (e.g., Windows 11 / Chrome 124)?
- Which version of TaskFlow are you on? (You can usually find this under Settings → About.)
- Roughly how many tasks do you have in your account?

That last one isn't strictly environment info, but since you mentioned having a lot of tasks, knowing the scale could help us pinpoint whether this is an indexing issue.

---

### reporter

Sure! I'm on Ubuntu 22.04 on a ThinkPad T14 with 32GB RAM. TaskFlow version is 2.3 — I actually upgraded from 2.2 about two weeks ago, which now that I think about it is right around when the slowness started. And I've got about 5,000 tasks built up over the last couple years.

---

### triage-agent

[RESOLVED] {"title":"Search on task description fields is 10-15s slower since v2.3 upgrade (~5k tasks)","problem":"Searching for keywords that match task description bodies takes 10-15 seconds to return results, while searches matching task titles return almost instantly. The reporter has ~5,000 tasks and the slowdown began around the time they upgraded from TaskFlow 2.2 to 2.3 approximately two weeks ago.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in how task description fields are indexed or queried. Possible causes: a full-text index on the description column was dropped or not migrated during the upgrade, the search query plan changed to a sequential scan on descriptions while titles retain an index, or a new search implementation in v2.3 handles description fields differently (e.g., dropped an index, changed to unoptimized LIKE/ILIKE queries, or broke an existing full-text search configuration).","reproduction_steps":["Create or use an account with a large number of tasks (~5,000) that have text in both title and description fields","Open the main search bar in TaskFlow 2.3","Search for a keyword (e.g., 'budget') that exists only in task descriptions, not titles","Observe that results take 10-15 seconds to return","Search for a keyword that exists in a task title","Observe that results return almost instantly"],"environment":"Ubuntu 22.04, ThinkPad T14, 32GB RAM, TaskFlow v2.3 (upgraded from v2.2 ~2 weeks ago), ~5,000 tasks","severity":"high","impact":"Any user with a significant number of tasks experiences severely degraded search performance when searching task descriptions — a core workflow. This is a regression from v2.3 affecting usability for power users.","recommended_fix":"Compare the database schema and query plan for description search between v2.2 and v2.3. Check whether a full-text index on the task description column was dropped or altered in the v2.3 migration. Run EXPLAIN ANALYZE on the search query with a description match vs. a title match to confirm the difference. If an index was dropped, restore it; if the query changed, revert or optimize the description search path.","proposed_test_case":"Create a test with 5,000+ tasks with known keywords in descriptions. Assert that a keyword search matching description text completes within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 schemas to confirm the regression and validate the fix.","information_gaps":["No server-side logs or query timing data to confirm whether the bottleneck is database, application, or network","Unknown whether other v2.3 users with large task counts experience the same issue","No information on database engine or configuration (PostgreSQL, SQLite, etc.)"]}

---
