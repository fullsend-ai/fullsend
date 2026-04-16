# Conversation: wrong-search-results x structured-triage

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — that 'Q2 planning' example is really helpful.

To help us reproduce this, could you walk us through the exact steps you're taking to search? Specifically:

- Are you using the main search bar, or a search/filter within a specific project or board?
- Are you typing the search term and hitting Enter, or selecting from autocomplete suggestions?
- Do you have any filters applied (e.g., date range, assignee, status) when you search?

Knowing the exact path you're taking will help us pinpoint where the results are going wrong.

---

### reporter

I'm using the main search bar at the top of the page. I just type my search term and hit Enter. I don't have any special filters applied — it's just the default view, which I believe is set to show active tasks only. That's what's so confusing: the default filter should be hiding archived tasks, but instead it seems like it's doing the opposite. I'm seeing all my old archived tasks in the results and my actual active tasks are missing. If I manually switch the filter to show archived tasks, I weirdly see my active ones there instead.

---

### triage-agent

Thanks for that extra detail — the fact that switching to the archived filter shows your *active* tasks instead is a really useful clue. It sounds like the filter logic may be inverted.

Could you share a few environment details so we can try to reproduce this on our end?

- What browser and version are you using? (e.g., Chrome 124, Firefox 126)
- What operating system? (e.g., Windows 11, macOS Sonoma)
- Do you know which version of TaskFlow you're on? (You can usually find this under Settings → About)

This will help us narrow down whether it's tied to a specific platform or a broader backend issue.

---

### reporter

I'm on Chrome, not sure of the exact version but it's up to date. Running Windows 11. TaskFlow version is 2.3.1 — we just got updated a few days ago actually. My teammate is on a Mac with Safari and sees the same issue, so I don't think it's browser-specific. I suspect it's related to that recent update since everything was working fine before it.

---

### triage-agent

[RESOLVED] {"title":"Search filter logic inverted: active filter returns archived tasks and vice versa (since v2.3.1)","problem":"After updating to TaskFlow 2.3.1, the search results filter is inverted. The default 'active tasks' filter returns archived tasks, and manually switching to the 'archived' filter returns active tasks. This affects all searches for all users.","root_cause_hypothesis":"The boolean or enum value controlling the active/archived filter predicate was likely inverted in the v2.3.1 update — either a flipped condition in the query logic, a reversed mapping between UI filter labels and backend query parameters, or a migration that swapped the status flag values.","reproduction_steps":["Log in to TaskFlow v2.3.1","Use the main search bar at the top of the page","Type a search term for a task that exists in both active and archived states (e.g., 'Q2 planning')","Hit Enter with default filters (active tasks only)","Observe that archived tasks appear in results and known active tasks are missing","Switch the filter to show archived tasks","Observe that active tasks now appear in results instead"],"environment":"TaskFlow v2.3.1 (issue began after this update). Reproduced on Chrome (latest)/Windows 11 and Safari/macOS. Likely platform-independent (backend issue).","severity":"high","impact":"All users are affected. Search is effectively unusable for finding active work — the core use case. Users must manually invert their filter selection as a workaround, which is confusing and error-prone.","recommended_fix":"Investigate the search filter logic introduced or modified in v2.3.1. Look for an inverted boolean condition, swapped enum values, or a negation error in the query that maps the active/archived UI filter to the backend task status. Check the diff between v2.3.0 and v2.3.1 for changes to the search query builder or task status filtering. A likely one-line fix (flipping a condition or removing an erroneous negation).","proposed_test_case":"Create one active task and one archived task with the same keyword. Search using default filters (active only) and assert the active task is returned and the archived task is not. Then switch to the archived filter and assert the archived task is returned and the active task is not.","information_gaps":["Exact commit or changeset in v2.3.1 that touched search or filter logic (requires codebase investigation)","Whether the inversion also affects non-search filtered views (e.g., project board filters)"]}

---
