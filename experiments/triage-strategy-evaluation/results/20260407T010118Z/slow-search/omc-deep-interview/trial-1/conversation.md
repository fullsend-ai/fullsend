# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down what's going on, could you tell us a bit more about what you're doing when the slowness happens? For example, are you using the search bar to search by keyword, filtering by tags/status/assignee, or something else? And does it happen with every search, or only with certain search terms or filters?

---

### reporter

It's the keyword search in the search bar. I notice it's mainly slow when I'm searching through task descriptions — like if I search for a phrase I know is in a task's description. Searching by title seems to work fine actually, those come back quick. It pretty much happens every time I do a description search, doesn't seem to matter what the search term is.

---

### triage-agent

That's really helpful — thanks for narrowing it down to description searches specifically.

Do you remember roughly when this started being slow? For instance, did it happen after a TaskFlow update, after you imported or created a large batch of tasks, or did it seem to get gradually worse over time? Anything that changed around the time you first noticed would help us zero in on the cause.

---

### reporter

I think it started around 2 weeks ago, maybe a bit more. I'm pretty sure that's around when I updated to v2.3 — I was on v2.2 before that. I didn't import a big batch of tasks or anything like that, it's been a steady ~5,000 tasks for a while now. So yeah, it might be related to the update but I'm not 100% sure on the exact timing.

---

### triage-agent

That timing with the v2.3 update is a useful clue — thanks.

I'd like to understand how much this is affecting your day-to-day work. Is searching task descriptions something you (or your team) rely on frequently, or is it more of an occasional thing? And have you found any workaround in the meantime — for example, do you just search by title instead, or is there information you can only find through description search?

---

### reporter

Yeah I search descriptions pretty regularly — I tend to dump meeting notes and detailed context into task descriptions, so sometimes the only way to find something is searching for a keyword that's in the description. I'd say I do it a few times a day at least. I've been mostly just searching by title as a workaround since that's still fast, but it's annoying because half the time what I need is buried in a description somewhere and title search won't find it. It's definitely slowing me down.

---

### triage-agent

Thanks — that confirms this is a real workflow problem, not just an occasional annoyance.

To help our developers reproduce this quickly: when you do a description search, does the delay feel roughly the same (~10-15 seconds) regardless of the search term, or do some searches come back faster than others? For example, is searching for a very common word just as slow as searching for something rare that might only be in one or two tasks?

Also, are you running TaskFlow as a desktop app, in a browser (and if so, which one), or self-hosted on a server? That'll help us set up a matching test environment.

---

### reporter

Hmm, I think it's roughly the same slowness no matter what I search for. Like even if I search for something unique that's probably only in one task, it still takes a long time. Maybe very slightly faster sometimes but nothing dramatic — it's always that 10-15 second range.

I'm running it as a desktop app on my laptop. It's a ThinkPad T14 running Ubuntu if that helps. I actually noticed my fan kicks up when I do a search, so I think it's working the CPU pretty hard for some reason.

---

### triage-agent

[RESOLVED] {"title":"Description keyword search regressed to ~10-15s in v2.3 (likely missing or dropped full-text index)","problem":"Since upgrading to v2.3, keyword searches that target task descriptions consistently take 10-15 seconds to return results and cause high CPU usage. Title-only searches remain fast. The slowness is independent of search term frequency or specificity.","root_cause_hypothesis":"The v2.3 update likely removed, broke, or failed to migrate a full-text index on the task description column (or switched the search implementation to a non-indexed path such as a LIKE/regex scan). The uniform latency regardless of term rarity and the CPU spike are consistent with a sequential scan over all ~5,000 task description blobs rather than an index lookup.","reproduction_steps":["Install TaskFlow v2.3 desktop app on Linux (Ubuntu, tested on ThinkPad T14)","Populate the database with ~5,000 tasks that have non-trivial description text","Perform a keyword search using the search bar that targets descriptions","Observe ~10-15 second response time and elevated CPU usage","Compare with the same dataset on v2.2 to confirm the regression"],"environment":"TaskFlow v2.3 desktop app, Ubuntu Linux, ThinkPad T14, ~5,000 tasks with detailed descriptions","severity":"high","impact":"Any user with a significant number of tasks who relies on description search is affected. The reporter uses it multiple times daily to locate meeting notes and context stored in descriptions. Title-only search is a partial workaround but insufficient for their workflow.","recommended_fix":"Diff the search/query layer between v2.2 and v2.3 for changes to how description search is executed. Check whether a full-text index on the description column exists in v2.3 databases (and in fresh installs vs. upgrades). If the index was dropped or the query path changed, restore indexed full-text search. Run EXPLAIN/query plan analysis on the description search query to confirm whether it's doing a sequential scan.","proposed_test_case":"Create a performance regression test: populate a database with 5,000+ tasks with realistic description text, execute a description keyword search, and assert the query completes in under 2 seconds. Run this test against both v2.2 and v2.3 schemas to catch index regressions.","information_gaps":["Exact database engine and schema used by the desktop app (SQLite, etc.) — dev team can inspect directly","Whether the regression also affects fresh v2.3 installs or only v2.2→v2.3 upgrades (migration issue vs. code change)","v2.3 changelog entries related to search or database schema changes"]}

---
