# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! The slowness you're describing definitely sounds frustrating.

Could you walk me through the steps to reproduce the slow search? Specifically:
- What search query or type of search are you running when it's slow? (e.g., keyword search, filtering by tags, searching within a specific project)
- Does it happen with every search, or only with certain queries?
- Roughly how many tasks do you have in your account?

This will help us narrow down where the bottleneck is.

---

### reporter

Sure! So it's slow when I search for keywords that would be in the task descriptions — like if I search for someone's name or a phrase from meeting notes I pasted in. But if I search for something that's in the task title, it comes back almost instantly. It happens every time I do a description search, not just certain queries. I have about 5,000 tasks built up over the last couple years.

---

### triage-agent

That's really helpful — the distinction between title search (fast) and description search (slow) is a great clue. Thanks for narrowing that down.

Could you share a few environment details so we can pin down when this might have started?
- What **version of TaskFlow** are you running? (You can usually find this under Settings → About)
- What **OS and browser** are you using? (e.g., Windows 11 / Chrome 124)

This will help us check whether the slowdown lines up with a specific release.

---

### reporter

I'm on version 2.3 — I actually upgraded from 2.2 about two weeks ago, and now that you mention it, I think the slowness started around that time. I'm running Ubuntu 22.04 on a ThinkPad T14. It's a desktop app so no browser involved.

---

### triage-agent

[RESOLVED] {"title":"Description search extremely slow (~10-15s) after upgrade to v2.3 with large task count","problem":"Searching by keywords that appear in task descriptions takes 10-15 seconds to return results, while searching for keywords in task titles returns almost instantly. The reporter has approximately 5,000 tasks and the slowness began around the time they upgraded from v2.2 to v2.3 approximately two weeks ago.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in how description search is executed — possibly a missing or dropped database index on the task description column, a change from indexed full-text search to unindexed LIKE/substring scanning, or a query planner change that causes a full table scan on descriptions but not titles.","reproduction_steps":["Have an account with a large number of tasks (~5,000)","Run TaskFlow v2.3 (desktop app)","Search for a keyword that appears in task descriptions but not in titles","Observe that results take 10-15 seconds to return","Search for a keyword that appears in task titles","Observe that results return almost instantly"],"environment":"TaskFlow v2.3 (desktop app), Ubuntu 22.04, ThinkPad T14, ~5,000 tasks, upgraded from v2.2 approximately two weeks ago","severity":"high","impact":"Any user with a substantial number of tasks who searches by description content experiences severe performance degradation, making the search feature effectively unusable for its primary purpose. This is a regression from v2.2.","recommended_fix":"Compare the v2.2 and v2.3 database schema and search query logic. Check for missing indexes on the task description column or changes to the search query (e.g., switching from full-text search to LIKE queries). Profile the description search query against a dataset of ~5,000 tasks to confirm the bottleneck. Restore or add appropriate indexing for description search.","proposed_test_case":"Create a test dataset with 5,000+ tasks containing varied description text. Execute a description keyword search and assert that results are returned within an acceptable threshold (e.g., under 1 second). Verify that both title and description searches scale similarly with large task counts.","information_gaps":["No error messages or logs were collected, though this is a performance issue rather than an error condition","Exact query mechanism (full-text search vs. substring match) used in v2.2 vs. v2.3 is unknown without checking the codebase","Whether other users with large task counts on v2.3 experience the same issue is unconfirmed"]}

---
