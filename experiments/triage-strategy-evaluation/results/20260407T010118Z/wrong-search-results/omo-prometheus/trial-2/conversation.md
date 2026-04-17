# Conversation: wrong-search-results x omo-prometheus

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — search returning stale results while missing active tasks is definitely something we need to dig into.

To start narrowing this down: was search working correctly for you before, and if so, when did you first notice the wrong results? For example, did this start after a recent update, or after a specific action like archiving those old tasks? Knowing whether this is a sudden change or something that's been gradually getting worse will help us zero in on the cause.

---

### reporter

Yes, search was working perfectly fine until about three days ago. That's when our team got the v2.3.1 update. I noticed the problem pretty much right after that patch went through. It was a sudden change, not gradual — one day search was fine, the next day it was returning all the wrong stuff. I have about 150 active tasks and 300 archived ones, so the bad results are really noticeable because I'm seeing way more results than I should when I search. It seems like the search is pulling up archived tasks instead of active ones.

---

### triage-agent

Thanks — the v2.3.1 timing is really helpful, that confirms this is a regression we can trace.

I want to understand the exact failure mode a bit more precisely: when you search for 'Q2 planning' and get results, are your active tasks **completely missing** from the list, or are they present but buried below the archived ones? And in the results you do see, do the archived tasks show any indication that they're archived (like a label or visual marker), or do they appear as if they're active?

---

### reporter

Good questions. When I search for 'Q2 planning', the active task I created yesterday is completely missing from the results — it's not buried, it's just not there at all. Instead I get the archived one from last year. And here's the weird part: when I look at the archived task in the results, it actually does still show as archived with the proper label. So the task itself knows it's archived, but the search is returning it anyway as if it's active. It's like the search filter is flipped — it's showing me archived tasks when I ask for active ones. If I toggle the filter to show archived tasks, I actually see my active tasks there instead. It's completely backwards.

---

### triage-agent

[RESOLVED] {"title":"Search filter for active/archived status is inverted since v2.3.1","problem":"Since the v2.3.1 update, the search index or query filter for task status (active vs. archived) is inverted. Searching with the default 'active' filter returns archived tasks and excludes active ones. Toggling the filter to 'archived' returns active tasks instead. The tasks themselves retain correct status labels — the inversion is in the search/filter layer, not the underlying data.","root_cause_hypothesis":"The v2.3.1 patch introduced a logical inversion in the search filter predicate for task status. Most likely a flipped boolean condition (e.g., `isArchived` vs `!isArchived`), a swapped enum mapping, or an inverted query parameter when filtering by status. The task metadata is correct (archived tasks still display the archived label), so the data layer is intact — the bug is in how the search query applies the status filter.","reproduction_steps":["Update to v2.3.1","Create or identify an active task with a known title (e.g., 'Q2 planning')","Ensure an archived task with a similar or matching title exists","Search for the title using the default (active) filter","Observe: archived task appears in results, active task is missing","Toggle the search filter to 'show archived'","Observe: active task now appears in the archived results"],"environment":"v2.3.1 (regression from previous version). User has ~150 active tasks and ~300 archived tasks. Confirmed by at least two users on the same team.","severity":"high","impact":"All users on v2.3.1 are affected. Search is functionally broken — users cannot find active tasks through search without manually inverting the filter. This degrades core usability for any user relying on search to navigate their task list.","recommended_fix":"Diff the search/filter logic between v2.3.0 and v2.3.1. Look for changes to the status filter predicate in the search query builder — likely a flipped boolean, negation, or swapped enum value when translating the UI filter selection into a database/index query. Fix the inversion. If a search index is involved, verify whether the index itself needs to be rebuilt or whether the bug is purely at query time (the fact that toggling the UI filter reverses behavior suggests it is query-time, not index-time).","proposed_test_case":"Create one active task and one archived task with the same title. Search with the 'active' filter and assert only the active task is returned. Search with the 'archived' filter and assert only the archived task is returned. Run this test against both v2.3.0 (should pass) and v2.3.1 (should fail pre-fix, pass post-fix).","information_gaps":["Which specific search interface is affected (web, mobile, API) — likely all, but not confirmed","Whether the bug is in the query builder, an API parameter mapping, or a UI-layer filter — narrowing this requires code inspection of the v2.3.1 diff"]}

---
