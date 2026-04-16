# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds really frustrating, especially when you know the task exists but search can't find it.

Can you help me understand the timeline a bit? Was search working correctly for you before this started happening, and if so, do you have a sense of roughly when it stopped giving you the right results? I'm curious whether this came on suddenly (like after an update or a specific event) or whether it's something you've been noticing gradually.

---

### reporter

Yes, search was definitely working fine before! It started about three days ago — I think that's right around when we got the v2.3.1 update. It was very sudden, not gradual at all. One day search was fine, the next day I started noticing that my search results were full of old archived tasks. I have about 300 archived tasks and only 150 active ones, so it's really noticeable — the results are dominated by stuff I archived months ago. It feels like it's related to that update somehow.

---

### triage-agent

That's really helpful — the timing with v2.3.1 is a strong clue.

I want to make sure I understand the 'missing active tasks' part correctly. When you searched for 'Q2 planning' and didn't see your active task — did you scroll through all the results to confirm it wasn't there at all, or is it more that the archived results pushed it so far down that you couldn't find it? I'm trying to understand whether active tasks are truly invisible to search, or whether they're just being outranked by archived ones.

Also, when you look at that active 'Q2 planning' task directly (not through search), does everything about it look normal — correct status, correct title, nothing odd?

---

### reporter

Good question — I went back and checked just now. When I search for 'Q2 planning', the active task I created yesterday genuinely does not appear in the results at all. It's not buried or pushed down, it's completely missing. The only result is the archived one from last year. And yes, when I go directly to the active 'Q2 planning' task (from my task list, not through search), it looks completely normal — correct title, marked as active, everything is fine. It's only search that seems to have the problem. Actually, I noticed something weird: if I flip the search filter to 'show archived only', I see my active tasks there instead! It's like the filter is backwards.

---

### triage-agent

[RESOLVED] {"title":"Search filter logic inverted in v2.3.1 — archived tasks shown as active and vice versa","problem":"Since the v2.3.1 update (~3 days ago), the search results filter is inverted: the default/active view returns archived tasks, and the 'show archived only' filter returns active tasks. Active tasks are completely absent from default search results, not merely deprioritized.","root_cause_hypothesis":"The v2.3.1 update introduced an inversion in the search filter predicate — likely a flipped boolean condition, negated flag, or swapped enum values in the query that partitions active vs. archived tasks. The underlying task data and status fields are correct (tasks display normally outside search), so this is purely a query/filter-layer bug.","reproduction_steps":["Create a new task with a distinctive title (e.g., 'filter test active') and leave it in active status","Archive a different task with a related title (e.g., 'filter test archived')","Open search and query for 'filter test' with the default (active) filter","Observe that only the archived task appears in results","Switch the search filter to 'show archived only'","Observe that the active task appears there instead"],"environment":"TaskFlow v2.3.1. Reproduced by at least two users (reporter and teammate). Reporter has ~300 archived tasks and ~150 active tasks. Not browser- or OS-specific based on multi-user reproduction.","severity":"high","impact":"All users are affected. Search is functionally broken — users cannot find active tasks through search at all, which is a core workflow. Users with large archives (like the reporter) are disproportionately impacted since results are dominated by irrelevant archived items.","recommended_fix":"Diff the search/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean, negated condition, or swapped enum/constant in the query that filters tasks by archived status. The fix is almost certainly a one-line predicate flip. Verify that both the default filter and the 'show archived only' filter use the correct status values.","proposed_test_case":"Unit test the search filter: given a set of tasks with known active/archived statuses, assert that querying with the 'active' filter returns only active tasks and querying with the 'archived' filter returns only archived tasks. Add a regression test that specifically checks a newly created active task appears in default search results.","information_gaps":["Whether the API/backend returns the wrong results or whether a frontend filter is inverting correct backend results (not critical for initial investigation — the developer can determine this from the v2.3.1 diff)","Whether other filter types (e.g., by priority, assignee) are also affected or only the archived/active partition"]}

---
