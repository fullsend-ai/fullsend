# Conversation: wrong-search-results x omc-deep-interview

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this! A couple of things that would help me understand the behavior better:

When you search for 'Q2 planning', do you see the archived task *instead of* the active one (i.e., the active one is completely missing from results), or do both appear but the archived one shouldn't be there at all? Also, roughly when did you first notice this — has search always behaved this way for you, or did it start recently (and if so, do you recall approximately when)?

---

### reporter

The active one is completely missing from the results — I only see the archived one. It's like the search has them swapped. If I specifically toggle the filter to show archived tasks, then I see my active ones there instead. It's the opposite of what it should be.

This started about three days ago. I think it was right around when we got the v2.3.1 update — there was some kind of database maintenance or migration that happened at the same time. Before that, search was working fine for me.

---

### triage-agent

That's a really helpful detail — the fact that toggling the archived filter flips the results is a strong clue.

One more thing that would help narrow down where the problem lives: when you browse your task list *without* using search (just viewing your project board or task list normally), do tasks show up in the correct categories? In other words, is it only *search* that has things backwards, or does the regular task list also show your active tasks under 'Archived' and vice versa?

---

### reporter

Good question — yes, when I just browse my task list or project board without using search, everything looks correct. Active tasks show as active, archived tasks show as archived. It's only when I use search that things are backwards. So the tasks themselves are fine, it's just the search filtering that's inverted.

---

### triage-agent

[RESOLVED] {"title":"Search archive filter is inverted since v2.3.1 — active tasks appear as archived and vice versa in search results","problem":"Since the v2.3.1 update (~3 days ago), the search feature returns inverted results with respect to the archived/active filter. Searching normally returns only archived tasks while hiding active ones. Toggling the 'show archived' filter flips the behavior, showing active tasks instead. The regular task list and project board display correct archive status — the inversion is isolated to search.","root_cause_hypothesis":"The database migration or code change in v2.3.1 most likely inverted the boolean logic for the archive filter in the search index or search query layer. Since the primary data store shows correct archive status (task list and project board are fine), the search index was either rebuilt with an inverted flag, or the search query's filter predicate was negated. Look for a WHERE clause, filter parameter, or index mapping related to 'archived'/'is_archived'/'status' in the search path that was changed in v2.3.1.","reproduction_steps":["Ensure you have at least one active task and one archived task with overlapping keywords (e.g., 'Q2 planning')","Use the search feature to search for that keyword with default filters (no archive toggle)","Observe that the archived task appears in results but the active task does not","Toggle the archive filter on","Observe that the active task now appears and the archived task disappears — the opposite of expected behavior","Confirm that viewing the project board or task list without search shows correct archive status"],"environment":"TaskFlow v2.3.1, reported by multiple users (at least two confirmed). Issue began approximately 3 days ago coinciding with the v2.3.1 update and an associated database migration.","severity":"high","impact":"All search results are inverted for affected users, making search effectively unusable for its intended purpose. At least two users confirmed affected; likely org-wide. A workaround exists (manually toggling the archive filter to get opposite behavior) but is unintuitive and error-prone.","recommended_fix":"Inspect the v2.3.1 migration and search-related code changes. Look for an inverted boolean in the search index build/rebuild logic (e.g., storing !is_archived instead of is_archived) or an inverted filter predicate in the search query path. If the search index is separate from the primary data store, rebuilding the index with the corrected flag mapping should fix existing data. The fix is likely a single boolean negation correction plus a reindex.","proposed_test_case":"Create one active task and one archived task with the same keyword. Search for that keyword with the default (non-archived) filter and assert that only the active task appears. Toggle to the archived filter and assert that only the archived task appears. Run this test against both the search path and the direct list/browse path to ensure consistency.","information_gaps":["Exact number of affected users — reporter confirmed herself and one teammate, but scope may be org-wide","Whether all search backends are affected (if TaskFlow uses multiple search providers or full-text vs. filtered search)","Exact migration script or changeset in v2.3.1 that touched search indexing (developer investigation item)"]}

---
