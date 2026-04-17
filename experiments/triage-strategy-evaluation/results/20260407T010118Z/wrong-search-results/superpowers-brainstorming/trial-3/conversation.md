# Conversation: wrong-search-results x superpowers-brainstorming

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this! To make sure I understand what you're expecting:

When you search for tasks, which of these best describes what you'd expect to happen?

1. **Archived tasks should never appear in search results** — search only covers active tasks
2. **Archived tasks should appear, but ranked lower** — active tasks should come first
3. **There's a filter or setting** that should be hiding archived tasks, but it's not working
4. **Something else**

This will help us figure out whether this is a bug in search indexing, a filtering issue, or something else entirely.

---

### reporter

Option 3 is the closest. There's a default filter that shows only active tasks, and it seems like it's doing the opposite of what it should. When I have the filter set to show active tasks, I'm seeing archived ones instead. And if I flip it to show archived tasks, I actually see my active ones. It's like the filter is inverted. I noticed this started about three days ago after we updated to v2.3.1.

---

### triage-agent

[RESOLVED] {"title":"Search active/archived filter is inverted since v2.3.1 — active filter returns archived tasks and vice versa","problem":"The search filter for task status (active vs. archived) is returning inverted results. When set to show active tasks, it returns archived tasks; when set to show archived tasks, it returns active ones. This affects all searches for all users and began after the v2.3.1 update approximately three days ago.","root_cause_hypothesis":"A logic inversion was introduced in v2.3.1 in the search filtering code — most likely a negated boolean condition, a swapped enum mapping, or an inverted query predicate for the active/archived status filter.","reproduction_steps":["Update to v2.3.1","Search for a term (e.g., 'Q2 planning') with the default active-tasks filter enabled","Observe that archived tasks appear in results while known active tasks are missing","Toggle the filter to show archived tasks","Observe that active tasks now appear instead"],"environment":"TaskFlow v2.3.1 (regression not present in prior version)","severity":"high","impact":"All users are affected — the default search experience returns wrong results, hiding active work and surfacing irrelevant archived tasks. This undermines core task-finding functionality.","recommended_fix":"Diff the search filter logic between v2.3.0 and v2.3.1. Look for the query or predicate that filters on task status (active/archived) — there is likely an inverted condition (e.g., `!isArchived` changed to `isArchived`, or a swapped enum value). Fix the inversion and add a regression test.","proposed_test_case":"Create one active task and one archived task with the same keyword. Search with the active filter enabled and assert the active task is returned and the archived task is not. Then search with the archived filter and assert the reverse. This test should run against both filter states to catch any future inversion.","information_gaps":["Whether other search filters (e.g., by assignee, priority, date) are also affected or only the active/archived filter","Whether the issue is in the frontend filter parameter or the backend query logic"]}

---
