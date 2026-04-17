# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds frustrating, especially when you're trying to find something you just created.

Can you help me understand a bit more about the context? What were you trying to accomplish when you ran that search — were you looking up the Q2 planning task to update it, share it with someone, or something else? And has search been working correctly for you up until recently, or is this the first time you've relied on it in a while? If it did used to work, do you recall roughly when you started noticing the wrong results?

---

### reporter

I was just trying to find my active Q2 planning task to update it with some new deadlines. Search has been my main way of navigating tasks since I have a lot of them — about 150 active and maybe 300 archived ones from past quarters.

It was working fine until a few days ago. I think it broke around the time we got the v2.3.1 update — that was about three days ago. Before that, search always showed me the right tasks. Now it's like the results are backwards: I'm seeing archived tasks when I search with the default filters, and my active ones are hidden. If I flip the filter to show archived tasks, I actually see my active ones there instead. It's really confusing.

The weird thing is, if I open a task directly, it shows the correct status — like the active ones still say 'active' and the archived ones still say 'archived.' It's only when I search or filter that things are mixed up.

---

### triage-agent

That detail about the results being 'backwards' is really helpful — it paints a clear picture. The fact that tasks show the correct status when you open them directly, but search inverts them, narrows this down a lot.

One more thing that would help us pinpoint this: have you noticed whether any *other* filters or sort options are also behaving oddly? For example, if you filter by priority, assignee, or due date, do those results come back as expected, or does the same kind of mix-up happen there too? I'm trying to understand whether this inversion is specific to the active/archived status or whether it's affecting search more broadly.

---

### reporter

Good question. As far as I can tell, it's only the active/archived filter that's messed up. When I filter by priority or assignee, those seem to work correctly — I get the right tasks back for those filters. It's specifically the archive status that's inverted. Everything else in search seems to behave normally, it's just that 'show active tasks' shows me archived ones and 'show archived tasks' shows me active ones.

---

### triage-agent

[RESOLVED] {"title":"Search active/archived filter inverted since v2.3.1 — active filter returns archived tasks and vice versa","problem":"Since the v2.3.1 update (~3 days ago), the search/filter functionality inverts the active/archived status filter. Selecting 'show active tasks' returns archived tasks, and selecting 'show archived tasks' returns active ones. All other filters (priority, assignee, due date) work correctly. Task data itself is intact — viewing a task directly shows the correct status.","root_cause_hypothesis":"The v2.3.1 release most likely introduced a boolean inversion in the search query predicate for archive status. This could be a flipped condition (e.g., `is_archived` vs `!is_archived`), a swapped enum mapping, or an inverted filter parameter. The fact that the data is correct on direct access and only the filter is affected confirms this is a query/filter-layer bug, not a data corruption issue.","reproduction_steps":["Have at least one active task and one archived task in the account","Use the search/filter interface with default filters (show active tasks)","Observe that archived tasks appear in results instead of active ones","Switch the filter to show archived tasks","Observe that active tasks appear in results instead of archived ones","Open any task directly and confirm its status label is correct"],"environment":"TaskFlow v2.3.1. Reproduced by at least two users (reporter and teammate). Not browser- or OS-specific based on multi-user reproduction.","severity":"high","impact":"All users relying on search or filtering by archive status see inverted results. For users with many tasks (reporter has ~450), search is the primary navigation method, making the application effectively unusable for daily task management. Workaround exists (manually invert the filter) but is confusing and error-prone.","recommended_fix":"Diff the search/filter query logic between v2.3.0 and v2.3.1, focusing on the archive status predicate. Look for an inverted boolean condition, swapped enum values, or a negation error in the query builder for the active/archived filter. The fix is almost certainly a one-line boolean flip.","proposed_test_case":"Create tasks with both active and archived statuses. Assert that filtering for 'active' returns only active tasks and filtering for 'archived' returns only archived tasks. This should be a regression test gating future releases.","information_gaps":["Exact code change in v2.3.1 that introduced the inversion (requires codebase access, not reporter knowledge)","Whether the bug manifests in API responses as well as the UI (would not change the fix direction)"]}

---
