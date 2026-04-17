# Conversation: wrong-search-results x structured-triage

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — that's really helpful context, especially the specific 'Q2 planning' example and the confirmation that your teammate sees the same behavior.

To help us investigate, could you share some environment details? Specifically:
- Are you using the web app, desktop app, or mobile app?
- Which browser and version (if web)?
- What version of TaskFlow are you on? (You can find this in Settings → About)

---

### reporter

I'm using the web app in Chrome (not sure of the exact version, but it's up to date). We're on TaskFlow v2.3.1 — we just updated a few days ago actually. Come to think of it, the search issues started right around when we did that update.

---

### triage-agent

[RESOLVED] {"title":"Search index includes archived tasks and excludes recently created active tasks after v2.3.1 upgrade","problem":"After upgrading to TaskFlow v2.3.1, search results include archived tasks that should be filtered out and fail to include recently created active tasks. The issue affects all searches and is reproducible by multiple users.","root_cause_hypothesis":"The v2.3.1 upgrade likely introduced a change to the search index or its filtering logic. Most probable causes: (1) a search index rebuild during the upgrade did not correctly apply the archived/active status filter, (2) a schema or query change in v2.3.1 removed or inverted the archived-task exclusion filter, or (3) newly created tasks post-upgrade are not being indexed due to a broken indexing pipeline.","reproduction_steps":["Upgrade to TaskFlow v2.3.1","Create a new active task with a distinctive name (e.g., 'Q2 planning')","Ensure an archived task with a similar or identical name exists","Use the search feature to search for that name","Observe that the archived task appears in results but the newly created active task does not"],"environment":"TaskFlow v2.3.1 (upgraded a few days ago), web app, Chrome (latest), confirmed across multiple users","severity":"high","impact":"All users are affected. Search is a core workflow feature — returning wrong results (stale archived tasks instead of active ones) undermines task discovery and could cause users to act on outdated information or miss current work items.","recommended_fix":"Investigate the search indexing pipeline in v2.3.1: (1) Check whether the search index was rebuilt during the upgrade and whether archived/active status was correctly indexed. (2) Review any query-level filters for archived task exclusion — compare v2.3.0 and v2.3.1 search query logic. (3) Verify that the post-upgrade indexing pipeline is picking up newly created tasks. (4) Check the v2.3.1 changelog for any search-related changes.","proposed_test_case":"Create an archived task and an active task with the same keyword. Perform a search for that keyword. Assert that only the active task appears in results and the archived task is excluded. Additionally, create a new active task and verify it appears in search results within the expected indexing latency.","information_gaps":["Exact Chrome version (unlikely to matter given this is a server-side search issue affecting multiple users)","Whether the issue also affects other search terms beyond 'Q2 planning' (reporter says 'all my searches', but no second specific example)","Whether the TaskFlow admin ran any manual index operations during the upgrade"]}

---
