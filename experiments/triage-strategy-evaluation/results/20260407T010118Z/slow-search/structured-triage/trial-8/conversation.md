# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I can see the search is taking 10-15 seconds when it used to be fast — that's definitely not expected.

To help us track this down, could you walk us through the steps to reproduce the slowness? Specifically:
- What are you searching for when it's slow? (e.g., a keyword, a tag, a filter combination)
- Where in the app are you searching? (e.g., the main search bar, a project-specific search)
- Does it happen with every search query, or only certain ones (like broad queries vs. specific ones)?

---

### reporter

I'm just searching from the main search bar at the top. It happens when I search for keywords that would be in the task descriptions — like searching for a project name or a topic from meeting notes I've pasted in. Actually now that I think about it, when I search for something that's in the task title it comes back pretty quickly. It's the description searches that seem to hang. I have about 5,000 tasks so maybe that's part of it, but it was fine before.

---

### triage-agent

Thanks for that detail — the distinction between title searches being fast and description searches hanging is really useful information.

Could you share a few environment details so we can try to reproduce this on our end?
- What OS is your work laptop running? (e.g., Windows 11, macOS Sonoma, Ubuntu 22.04)
- Which browser and version are you using?
- What version of TaskFlow are you on? (You can usually find this under Settings → About)

---

### reporter

I'm on Ubuntu 22.04, ThinkPad T14 with 32GB RAM. TaskFlow version is 2.3 — I actually upgraded from 2.2 maybe two weeks ago, and I think the slowness might have started around that time but I'm not 100% sure. As for the browser, I'm not sure which one matters here since I use the desktop app. Or do you mean something else?

---

### triage-agent

[RESOLVED] {"title":"Search on task descriptions extremely slow (~10-15s) after upgrade to v2.3 with large dataset","problem":"Full-text search against task descriptions via the main search bar takes 10-15 seconds to return results, while searches matching task titles return quickly. The reporter has approximately 5,000 tasks and believes the slowness began around the time they upgraded from TaskFlow 2.2 to 2.3 roughly two weeks ago.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in description search — possibly a missing or dropped full-text index on the task descriptions column/field, a query plan change, or a switch from indexed search to unoptimized LIKE/scan-based search for description content. The fact that title searches remain fast suggests the title field still has proper indexing while the description field does not.","reproduction_steps":["Set up a TaskFlow instance running version 2.3 (desktop app)","Populate the database with ~5,000 tasks that have substantive text in their descriptions","Open the main search bar at the top of the app","Search for a keyword that appears only in task descriptions (not titles)","Observe that the search takes 10-15 seconds to return results","Search for a keyword that appears in a task title and observe it returns quickly"],"environment":"Ubuntu 22.04, Lenovo ThinkPad T14, 32GB RAM, TaskFlow v2.3 desktop app (upgraded from v2.2 approximately two weeks ago)","severity":"medium","impact":"Users with large task databases (~5,000+ tasks) experience severe search latency on description queries, degrading a core workflow. Likely affects all users at scale on v2.3.","recommended_fix":"Compare the search query path and indexing for task descriptions between v2.2 and v2.3. Check whether a full-text index on the descriptions field was dropped, altered, or is no longer being used by the query planner. Review the v2.3 migration scripts and any ORM/schema changes that touch the search or descriptions columns.","proposed_test_case":"Create a test with 5,000+ tasks with varied description text. Assert that a keyword search matching only descriptions completes within an acceptable threshold (e.g., <1 second). Run this test against both v2.2 and v2.3 schemas to confirm the regression.","information_gaps":["Exact timing of when slowness started relative to the v2.2→v2.3 upgrade (reporter is not 100% certain)","Whether any error messages or warnings appear in application logs during slow searches","Whether the desktop app uses an embedded database (SQLite) or a remote backend — this affects which indexing approach to investigate"]}

---
