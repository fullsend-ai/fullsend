# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds frustrating, especially when you know the task exists but search won't surface it.

To help me understand the full picture: can you walk me through what you were doing when you noticed this? Specifically, had search been working correctly for you before, and if so, do you have a sense of when it stopped — was it sudden (e.g., after an update or a specific action you took), or something you noticed gradually?

---

### reporter

Yes, search was working perfectly fine up until about three days ago. It broke right after the system update — I believe it was the v2.3.1 patch that went out. Before that, searching for tasks worked exactly as expected. After the update, it was immediately wrong. I noticed it the same day because I rely on search heavily to find my active tasks. The strange thing is that it seems like the archive filter is backwards — when I have 'active tasks only' selected (which is the default), I'm seeing archived tasks instead. And if I flip the filter to show archived tasks, I actually see my active ones. It's like the filter is inverted. Individual tasks still show the correct status when I open them, so it's just the search/filter that's broken.

---

### triage-agent

[RESOLVED] {"title":"Search filter inverted since v2.3.1 — 'active tasks only' returns archived tasks and vice versa","problem":"Since the v2.3.1 patch (approximately three days ago), the search filter for task status is inverted. When 'active tasks only' is selected (the default), archived tasks are returned instead. When the filter is switched to show archived tasks, active tasks appear. The underlying task status data is correct — individual tasks display their correct status — so the bug is isolated to the search/filter query logic.","root_cause_hypothesis":"The v2.3.1 patch likely introduced a logic inversion in the search filter predicate — either a negated boolean condition, a swapped enum mapping, or an inverted comparison when translating the UI filter selection into the search query. A diff of the search query construction between v2.3.0 and v2.3.1 should reveal the change.","reproduction_steps":["Update to v2.3.1 (or use any current instance)","Create or confirm the existence of an active task with a known title (e.g., 'Q2 planning')","Open search with the default filter ('active tasks only')","Search for the known active task's title","Observe that the active task is missing and archived tasks with matching terms appear instead","Switch the filter to 'archived tasks' and repeat the search","Observe that the active task now appears in the archived results"],"environment":"Affects all users on v2.3.1. Confirmed by reporter and at least one teammate. Not account-specific.","severity":"high","impact":"All users relying on search (which uses 'active tasks only' as the default filter) see incorrect results. Active tasks are effectively hidden by default, and archived tasks pollute results. This breaks a core workflow for anyone who uses search to find their current tasks.","recommended_fix":"Diff the search filter/query logic between v2.3.0 and v2.3.1. Look for an inverted boolean, negated condition, or swapped enum value in the code that translates the UI's active/archived filter into the database or search index query. The fix is likely a one-line logic inversion correction.","proposed_test_case":"Create both an active and an archived task with the same keyword. Search with the 'active tasks only' filter and assert only the active task is returned. Search with the 'archived tasks' filter and assert only the archived task is returned. Run this test against both v2.3.0 (should pass) and v2.3.1 (should fail pre-fix, pass post-fix).","information_gaps":["Exact code change in v2.3.1 that introduced the inversion (requires codebase inspection, not reporter input)","Whether the bug also affects API-level search or only the UI (tangential to fixing the core issue)"]}

---
