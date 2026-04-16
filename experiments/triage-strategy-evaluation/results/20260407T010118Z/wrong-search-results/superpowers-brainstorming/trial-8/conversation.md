# Conversation: wrong-search-results x superpowers-brainstorming

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this! To make sure I understand the expected behavior — when you search for tasks, which of these best describes what you'd expect to happen?

1. **Archived tasks should never appear in search results** — search should only return active tasks
2. **Archived tasks should appear, but ranked lower** — active tasks should always come first
3. **There's a filter/toggle for archived tasks** that you had set to exclude them, but they're showing up anyway
4. **Something else**

This will help us narrow down whether this is a filtering bug, a ranking bug, or something else entirely.

---

### reporter

Option 3 is closest to what I'm experiencing. There's a default filter that shows only active tasks, and that's what I normally use. But the results I'm getting when that filter is on are wrong — I'm seeing archived tasks showing up as if they're active, and my actual active tasks are missing. If I switch the filter to show archived tasks, I actually see my active ones there instead. It's like the filter is completely backwards. The weird thing is, when I click into any individual task, it correctly shows whether it's active or archived — it's just the search filter that's mixed up.

---

### triage-agent

[RESOLVED] {"title":"Search filter for active/archived tasks is inverted — active filter returns archived tasks and vice versa","problem":"The search task-status filter is applying the opposite predicate. When a user selects the default 'active tasks' filter, search returns archived tasks instead. When they switch the filter to 'archived tasks', they see their active tasks. The individual task detail view correctly displays each task's actual status, confirming the underlying data is correct — the inversion is isolated to the search/filter layer.","root_cause_hypothesis":"The search query or filter predicate for the active/archived status flag is inverted. Most likely a boolean negation error or swapped enum values in the search filter logic — e.g., the filter passes `archived = true` when the user selects 'active', or the mapping between the UI filter option and the query parameter is reversed.","reproduction_steps":["Create or identify a task that is in 'active' status","Go to the search/task list view with the default filter set to 'Active tasks'","Search for the task by name — it will not appear in results","Switch the filter to 'Archived tasks'","The active task now appears in the archived filter results","Click into the task — its detail view correctly shows it as 'active'"],"environment":"Affects all users (confirmed by reporter and at least one teammate). Not browser- or account-specific. The default 'active tasks' filter is the affected path.","severity":"high","impact":"All users searching for tasks see inverted results by default. Active tasks are effectively hidden from normal search, and archived tasks pollute results. This undermines core usability of the task search feature for everyone.","recommended_fix":"Investigate the search filter logic where the active/archived status predicate is constructed. Look for an inverted boolean or swapped enum mapping between the UI filter selection and the database/search query. Likely a one-line fix — either a negation that shouldn't be there, or two enum values that are swapped in a mapping. Check recent changes to the search filter or task status model for a regression.","proposed_test_case":"Create one active task and one archived task with the same keyword in their titles. Search with the 'active' filter — assert only the active task is returned. Search with the 'archived' filter — assert only the archived task is returned. Verify the fix with both filter states.","information_gaps":["When this behavior started (recent deploy regression vs. longstanding bug) — useful for git bisect but not required for the fix","Whether the inversion also affects other filter types (e.g., by priority, by assignee) or is isolated to the active/archived filter"]}

---
