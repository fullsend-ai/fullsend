# Conversation: wrong-search-results x superpowers-brainstorming

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down the cause — was search working correctly for you before, and this is a recent change? For example:

1. **It used to work fine** and broke recently (within the last few days/weeks)
2. **It's been unreliable for a while** but you're only now reporting it
3. **You're not sure** — you only recently started relying on search heavily

This will help us figure out whether we're looking for a recent regression or a longer-standing bug.

---

### reporter

It definitely used to work fine and broke recently. I'd say it started about three days ago. That lines up with the v2.3.1 update — I remember getting a notification about a patch being applied. Before that, search was working perfectly. Now it's like the archive filter is backwards: when I search with the default 'active tasks only' filter, I'm getting archived tasks instead, and if I flip it to show archived tasks, I see my active ones. It's very consistent, not intermittent.

---

### triage-agent

[RESOLVED] {"title":"Search archive filter inverted since v2.3.1 — 'active tasks only' returns archived tasks and vice versa","problem":"Since the v2.3.1 patch (~3 days ago), the search archive filter behaves inversely: searching with the default 'active tasks only' filter returns archived tasks, while switching to the archived filter shows active tasks. This affects all searches for all users.","root_cause_hypothesis":"The v2.3.1 patch likely inverted the boolean logic or enum mapping for the archive status filter in the search query. This could be a negated condition, a swapped enum value, or an inverted boolean flag in the search index query.","reproduction_steps":["Search for any term (e.g., 'Q2 planning') with the default 'active tasks only' filter","Observe that results include archived tasks and exclude known active tasks","Switch the filter to 'show archived tasks'","Observe that active tasks now appear in results"],"environment":"v2.3.1 (patch applied ~3 days ago). Affects multiple users.","severity":"high","impact":"All users are affected. Search — a core feature — is functionally broken: users cannot find active tasks without manually inverting the filter, and default search results are misleading.","recommended_fix":"Diff the search filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean condition, swapped enum/constant, or negated predicate in the archive status filter applied to search queries. The fix is likely a one-line boolean or comparison inversion.","proposed_test_case":"Create one active task and one archived task with overlapping keywords. Search with 'active only' filter and assert the active task is returned and the archived task is not. Search with 'archived' filter and assert the inverse. This test should be run against both filter states to catch inversions.","information_gaps":["Exact code path changed in v2.3.1 (developer will identify from the diff)"]}

---
