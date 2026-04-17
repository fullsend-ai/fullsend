# Conversation: wrong-search-results x omo-prometheus

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — search returning stale results while missing active tasks is definitely disruptive.

To start narrowing this down: **was search working correctly for you before, and if so, when did you first notice the problem?** Specifically:

- Did searching for 'Q2 planning' (or similar terms) return correct results at some point, or has this always been off?
- Are you searching via the web UI, the mobile app, or the API?
- When you say archived tasks "should be hidden" — did you archive them yourself, or were they auto-archived by a rule?

This will help me figure out whether we're looking at a regression in the search index or a filtering/visibility issue.

---

### reporter

Yes, search was definitely working correctly before. I first noticed the problem about three days ago — right around when we got the v2.3.1 update. Before that, searching for tasks worked perfectly and I never saw archived ones showing up unexpectedly.

I'm using the web UI for searching. And the archived tasks I'm seeing in results were ones I archived manually over the past several months — old project tasks I completed and archived myself.

One thing I want to clarify: it's not just that archived tasks are showing up — it seems like the filter is actually inverted. When I have the default 'active tasks only' filter on, I'm mostly seeing archived tasks. And when I switch the filter to show archived tasks, I see my active ones instead. It's really confusing. I have about 150 active tasks and 300 archived ones, so when I search with the default filter I'm getting way more results than I'd expect, and they're mostly old stuff.

---

### triage-agent

That's a really helpful observation — a filter inversion is a much more specific bug than a general search issue, and the timing with v2.3.1 strongly suggests a regression.

To confirm the pattern before I write this up for the team: **does the inversion hold consistently across different searches, or only for certain queries?** Specifically:

1. If you search for a term that you know only exists in an active task (something you created recently), does it return zero results with the default 'active tasks only' filter — but appear when you switch to the 'archived' filter?
2. Does the same inversion happen on the **main task list view** (without searching), or is it only when you use the search box?

This will tell us whether the bug is in the search filtering specifically or in a broader filter logic change in v2.3.1.

---

### reporter

Yes, the inversion is completely consistent across all my searches. I just tested what you described — I searched for the name of a task I created two days ago, and with the default 'active tasks only' filter, it doesn't show up at all. But when I switch to the 'archived' filter, there it is. So yes, it's definitely inverted across the board, not just for certain queries.

As for the main task list view without searching — no, that actually looks correct. When I go to my task list and browse without using search, the active tasks show as active and the archived ones are hidden as expected. If I click into an individual task, it correctly shows whether it's active or archived. The inversion only happens when I use the search functionality with the archive filter.

---

### triage-agent

[RESOLVED] {"title":"Search archive filter is inverted since v2.3.1 — 'active only' returns archived tasks and vice versa","problem":"Since the v2.3.1 update (~3 days ago), the search functionality's archive status filter is inverted. Searching with the default 'active tasks only' filter returns archived tasks and omits active ones. Switching to the 'archived' filter shows active tasks instead. The main task list view and individual task detail views are unaffected — the inversion is isolated to search.","root_cause_hypothesis":"The v2.3.1 release likely introduced a boolean inversion or enum swap in the search-specific filtering logic. Since the main task list uses a different code path for filtering and works correctly, the bug is in the search module's filter application — most likely a negated boolean (e.g., `!isArchived` where `isArchived` was intended), swapped enum/parameter values, or an inverted query predicate in the search index query builder. A diff of search filter logic between v2.3.0 and v2.3.1 should reveal the regression immediately.","reproduction_steps":["Log in to the web UI as any user with both active and archived tasks","Use the search box to search for a term matching a known active task (e.g., a task created recently)","Observe that with the default 'active tasks only' filter, the active task does not appear but archived tasks matching the query do","Switch the filter to 'archived'","Observe that the active task now appears in results","Confirm that browsing the main task list (without search) shows correct active/archived separation"],"environment":"Web UI, v2.3.1. Reproduced by at least two users (reporter and teammate). Reporter has ~150 active and ~300 archived tasks. Archived tasks were manually archived over several months.","severity":"high","impact":"All users searching with the archive filter are affected. Search is functionally broken for its primary use case (finding active tasks). Users can work around it by manually inverting the filter, but this is confusing and error-prone. The main task list view is unaffected, so basic task browsing still works.","recommended_fix":"Diff the search filter/query logic between v2.3.0 and v2.3.1. Look for changes to how the archive status filter parameter is applied in the search query builder — likely a boolean negation, inverted conditional, or swapped enum mapping. Fix the inversion and verify against both the 'active only' and 'archived' filter states.","proposed_test_case":"Create a test with a mix of active and archived tasks. Execute a search with the 'active only' filter and assert that only active tasks are returned. Execute the same search with the 'archived' filter and assert that only archived tasks are returned. Add this as a regression test gated on the filter parameter to prevent future inversions.","information_gaps":["Whether the teammate's experience is identical or subtly different (e.g., different task counts or filter combinations)","Whether the API endpoint backing search also exhibits the inversion (would confirm it's backend, not frontend)","Whether any other filters (e.g., by project, priority, assignee) are similarly affected in search"]}

---
