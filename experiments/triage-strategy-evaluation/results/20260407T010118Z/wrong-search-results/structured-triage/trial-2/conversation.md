# Conversation: wrong-search-results x structured-triage

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that definitely sounds disruptive, especially if it's affecting your teammate as well.

To help us track this down, could you walk us through the exact steps to reproduce? For example:

1. Where are you performing the search — the main search bar, a filtered view, or somewhere else?
2. What exactly do you type in the search field?
3. Do you have any filters or sorting applied when you search?

Knowing the precise path you're taking will help us narrow down whether this is a search indexing issue or a filtering problem.

---

### reporter

Sure! I'm using the main search bar at the top of the dashboard. I just type in keywords like 'Q2 planning' or a task name. I don't usually change any filters — I just leave it on the defaults, which I believe is set to show active tasks only. That's what's so confusing: the default filter should be hiding archived tasks, but instead it seems like it's doing the opposite. I'm seeing old archived tasks in my results and my actual active tasks are missing. If I switch the filter to 'show archived,' I actually see my active tasks there, which makes no sense. I have about 150 active tasks and 300 archived ones, so it's really noticeable — my search results are flooded with old stuff.

---

### triage-agent

Thanks for those details — the filter inversion behavior is a really helpful clue. It sounds like the search index or filter logic may have the active/archived flag flipped.

Before we hand this off to the team, could you share your environment details? Specifically:

- **App version** (you can usually find this in Settings → About, or the bottom of the sidebar)
- **Browser and version** (e.g., Chrome 124, Firefox 126)
- **Operating system** (e.g., Windows 11, macOS 14)

This will help us determine whether this is tied to a recent release or something environment-specific.

---

### reporter

The app version is v2.3.1 — we updated about three days ago. I'm using Chrome (not sure of the exact version, maybe 124 or 125?) on Windows 11. My teammate is on a Mac with Safari and sees the same issue, so I don't think it's browser-specific. I do remember the search was working fine before the update, so this might be related to whatever changed in v2.3.1.

---

### triage-agent

[RESOLVED] {"title":"Search filter inversion: active/archived flag is flipped since v2.3.1","problem":"Since updating to v2.3.1 (approximately three days ago), the search filter for active vs. archived tasks is inverted. The default 'active tasks only' filter returns archived tasks, while the 'show archived' filter returns active tasks. This affects all searches for all users.","root_cause_hypothesis":"The v2.3.1 update likely introduced a boolean inversion in the search filter logic — the active/archived predicate is negated, so 'active' queries the archived flag and vice versa. This could be in the search query builder, the index mapping, or a filter parameter that was flipped during the release.","reproduction_steps":["Log into TaskFlow (v2.3.1)","Navigate to the main dashboard","Use the top search bar to search for a known term (e.g., 'Q2 planning') with default filters (active tasks only)","Observe that results contain archived tasks and omit known active tasks","Switch the filter to 'show archived'","Observe that active tasks now appear in the results"],"environment":"App version v2.3.1 (updated ~3 days ago). Reproduced on Chrome ~124-125 / Windows 11 and Safari / macOS — not browser-specific. User has ~150 active and ~300 archived tasks.","severity":"high","impact":"All users are affected. Search is effectively unusable under default settings — users see irrelevant archived tasks and cannot find their active work. Workaround exists (manually toggle to 'show archived' to find active tasks) but is counterintuitive and unreliable for users who don't know about it.","recommended_fix":"Investigate the search filter logic changed in v2.3.1. Look for a boolean inversion in the active/archived predicate — likely a negation bug (e.g., `!isArchived` replaced with `isArchived`, or a flipped enum/query parameter). Check the search query builder, any ORM filter, or Elasticsearch/database query where the archived status is applied. A diff of the search filter code between v2.3.0 and v2.3.1 should surface the change quickly.","proposed_test_case":"Create one active task and one archived task with a shared keyword. Search using default filters ('active only') and assert that only the active task appears. Switch to 'show archived' and assert that only the archived task appears. This test should cover both the search endpoint and the UI filter parameter passed to it.","information_gaps":["Exact Chrome version (minor; confirmed not browser-specific)","Whether the issue affects other filter combinations beyond active/archived (e.g., by project, by assignee)","Server-side logs or query traces showing the actual filter parameters being sent"]}

---
