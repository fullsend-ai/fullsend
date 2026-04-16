# Conversation: wrong-search-results x structured-triage

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — the example with the 'Q2 planning' search is really helpful for narrowing this down.

Could you let us know what environment you're using? Specifically:
- Are you using the web app, desktop app, or mobile app?
- What browser and version (if web)?
- What version of TaskFlow are you on? (You can find this under Settings → About)

---

### reporter

I'm using the web app in Chrome (not sure of the exact version, but it's up to date). We're on TaskFlow v2.3.1 — we just updated a few days ago actually. Come to think of it, the search issues started right around the time we did that update.

---

### triage-agent

[RESOLVED] {"title":"Search index includes archived tasks and omits recently created active tasks since v2.3.1 upgrade","problem":"After upgrading to TaskFlow v2.3.1, search results include archived tasks that should be filtered out and fail to surface recently created active tasks. The issue affects all searches for at least two users on the same instance.","root_cause_hypothesis":"The v2.3.1 upgrade likely introduced a search index regression — either the index was rebuilt without respecting the archived/active status filter, or a schema migration changed how task status is indexed. A reindex operation after the upgrade may have included archived tasks or failed to index new active tasks correctly.","reproduction_steps":["Ensure TaskFlow is on v2.3.1","Create a new active task with a distinctive name (e.g., 'Q2 planning')","Ensure an archived task with the same or similar name exists","Use the search feature to search for that name","Observe that the archived task appears in results and the newly created active task does not"],"environment":"TaskFlow v2.3.1 (upgraded a few days ago), web app, Chrome (latest), multiple users affected","severity":"high","impact":"All users on the instance are affected — search is a core workflow feature and currently returns misleading results, making it unreliable for finding active work.","recommended_fix":"Investigate the v2.3.1 changelog for changes to search indexing, task status filtering, or index migration. Compare the search query logic between v2.3.0 and v2.3.1 for how archived status is handled. Check whether a post-upgrade reindex is needed and whether newly created tasks are being indexed at all. If the search index is stale or corrupt, triggering a full reindex with correct status filtering may resolve the issue immediately.","proposed_test_case":"Create an archived task and an active task with the same keyword. Perform a search for that keyword and assert that only the active task appears in results. Additionally, create a new active task and verify it appears in search results within the expected indexing delay.","information_gaps":["Exact Chrome version (unlikely to matter given this appears server-side)","Whether the issue also affects other search terms beyond 'Q2 planning' with concrete examples (reporter stated all searches are affected)","Server-side logs or search index state that could confirm the reindex hypothesis"]}

---
