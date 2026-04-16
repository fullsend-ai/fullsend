# Conversation: wrong-search-results x omo-prometheus

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — that's frustrating, especially when you can't trust search results.

To start narrowing this down: was search working correctly for you before, and if so, approximately when did you first notice the wrong results? I'm trying to determine whether this is a recent regression or something that's been building up. Also, are you using the web app, mobile app, or API when searching?

---

### reporter

Yes, search was definitely working fine before. I first noticed the problem about three days ago — it seems like it started right after the v2.3.1 update was applied. Before that, search was reliable and I never had issues finding my active tasks. I'm using the web app.

---

### triage-agent

Thanks — that's really helpful. Pinning it to the v2.3.1 update gives us a clear starting point.

I want to understand the failure mode more precisely. When you search for 'Q2 planning' and get the archived task back:

1. Does the archived task show up **with** any kind of 'archived' label or indicator, or does it look like a normal active task in the results?
2. If you go to your task list (without using search) and browse to the active 'Q2 planning' task — is it still there and visible, just not appearing in search results?

This will help me distinguish between a search indexing problem (new tasks aren't being indexed) versus a filtering problem (the query is pulling from the wrong pool of tasks).

---

### reporter

Good questions. For #1 — the archived task that shows up in search results does still have the 'archived' label on it. It's not like it lost its archived status. It just shouldn't be appearing in my search results since I have the default 'active tasks only' filter on. For #2 — yes, exactly. If I go browse my task list directly without using search, the active 'Q2 planning' task is right there. It exists and it's marked as active. It just doesn't come up when I search for it. So it seems like the search filter is somehow backwards — it's showing me archived tasks when I ask for active ones, and hiding the active ones.

---

### triage-agent

Your description really does sound like the filter got flipped. I'd like to test that theory directly — could you try one quick experiment?

In the search interface, manually switch the filter from 'active tasks only' to 'archived tasks only' (or 'all tasks,' if that's an option) and search for 'Q2 planning' again. I want to see:

- With the filter set to **'archived only'**: does your *active* Q2 planning task show up?
- With the filter set to **'all tasks'** (if available): do you see *both* the active and archived versions?

This will tell us whether the filter logic is literally inverted versus something else going on with the search index. It'll save the engineering team a lot of investigation time.

---

### reporter

I just tried that! With the filter set to 'archived only,' my active Q2 planning task does show up — that's the one I created recently and it's definitely not archived. And when I set it to 'all tasks,' I see both the active and archived versions together. So it really does seem like the filter is completely inverted — 'active only' shows archived tasks, and 'archived only' shows active tasks. This started right after the v2.3.1 update, which I believe included some kind of database migration.

---

### triage-agent

[RESOLVED] {"title":"Search status filter is inverted since v2.3.1 — 'active only' returns archived tasks and vice versa","problem":"The search feature's task status filter is returning inverted results. Searching with the 'active tasks only' filter returns archived tasks, while searching with 'archived tasks only' returns active tasks. The 'all tasks' filter works correctly, showing both. Task status labels are correct on the tasks themselves, and browsing tasks without search works fine. Multiple users are affected.","root_cause_hypothesis":"The v2.3.1 update included a database migration that most likely inverted the boolean or enum value representing task status in the search index or query layer. The filter predicate for active/archived status is applying the opposite condition — e.g., a boolean flag was flipped, an enum mapping was swapped, or a NOT condition was introduced/removed in the search query. The task records themselves are correct (browsing works, labels are accurate), so this is isolated to the search query filtering logic, not the underlying data.","reproduction_steps":["Log into the TaskFlow web app (any user account)","Ensure the search filter is set to 'active tasks only' (the default)","Search for a term that matches both an active and an archived task (e.g., 'Q2 planning')","Observe that the archived task appears and the active task does not","Switch the filter to 'archived tasks only' and repeat the search","Observe that the active task now appears instead","Switch the filter to 'all tasks' — both appear correctly"],"environment":"Web app, post v2.3.1 update (deployed ~3 days ago). Confirmed by at least two users.","severity":"high","impact":"All users relying on search are affected. The default search experience ('active tasks only') hides all active tasks and surfaces archived ones instead. This undermines basic usability — users cannot find their current work through search. Workaround exists: users can set the filter to 'all tasks' and visually scan results.","recommended_fix":"Examine the v2.3.1 changeset, focusing on the database migration and any changes to the search query layer's status filter logic. Look for an inverted boolean, a swapped enum mapping, or a flipped conditional (e.g., `status = 'archived'` where it should be `status = 'active'`, or a negation that was added/removed). The fix is likely a one-line correction in the search query builder or a migration fix to correct the indexed values. Verify that direct task browsing uses a different query path (since it works correctly).","proposed_test_case":"Create one active task and one archived task with the same keyword in their title. Execute a search with the 'active only' filter and assert only the active task is returned. Execute a search with 'archived only' and assert only the archived task is returned. Execute with 'all tasks' and assert both are returned. This test should cover the search query builder's filter application.","information_gaps":["Whether the API search endpoint exhibits the same inverted behavior (likely, but unconfirmed)","Whether other search filters (e.g., by assignee, priority, date) are also affected or only the status filter","The exact nature of the v2.3.1 database migration (the dev team has this information)","Whether mobile app search is also affected"]}

---
