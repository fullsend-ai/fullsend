# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds really frustrating, especially when you know the task exists but search won't surface it.

To help me understand the full picture: can you walk me through what you were doing when you noticed this? Specifically, were you searching from a particular view or project, and had you recently made any changes — like archiving tasks, switching projects, or updating any filters or settings? Also, you mentioned this started happening for "all your searches" — does that mean it was working correctly before? If so, do you have a rough sense of when it stopped working as expected?

---

### reporter

Sure! I was just doing a normal search from the main Tasks view — nothing fancy, no special project filter. I hadn't changed any settings or filters myself. The default 'Active tasks only' filter was on, which is what I always use.

It was definitely working fine before. I noticed it started about three days ago, right after our team got the v2.3.1 update. I remember because there was a database migration that ran as part of that patch. Ever since then, my searches have been off.

What's really confusing is that when I look at an individual task, it correctly shows whether it's active or archived. But search seems to have it backwards — when I filter for active tasks, I'm seeing archived ones, and if I flip the filter to show archived tasks, I see my active ones instead. I have about 150 active tasks and 300 archived ones, so it's very noticeable because I'm suddenly seeing way more results than I expect when filtering for 'active only.'

---

### triage-agent

[RESOLVED] {"title":"Search active/archived filter inverted after v2.3.1 database migration","problem":"After the v2.3.1 update (which included a database migration), the search filter for task status is inverted: filtering for 'active' tasks returns archived tasks, and filtering for 'archived' tasks returns active ones. Individual task detail views show the correct status, indicating the underlying task data is intact but the search index or filter predicate is reversed.","root_cause_hypothesis":"The v2.3.1 database migration likely inverted a boolean/enum value in the search index (e.g., flipping an is_archived flag, swapping status enum values, or inverting the filter predicate logic). Since individual task views read from the primary data store and show correct status, the problem is isolated to the search layer — either the search index was rebuilt with inverted status values, or the filter query logic was changed to negate the condition.","reproduction_steps":["Update to v2.3.1 (ensure the database migration has run)","Navigate to the main Tasks view","Ensure the default 'Active tasks only' filter is selected","Search for a term that matches both active and archived tasks (e.g., 'Q2 planning')","Observe that archived tasks appear in results while known active tasks are missing","Switch the filter to 'Archived tasks' and observe that active tasks now appear instead"],"environment":"TaskFlow v2.3.1, post-database-migration. Affects multiple users on the same team. Observed from the main Tasks view using default search filters.","severity":"high","impact":"All users on v2.3.1 are affected. Search — a core workflow feature — returns incorrect results, making it unreliable for finding active work. Users with large task counts (e.g., 150 active / 300 archived) see dramatically wrong result sets, undermining trust in the tool.","recommended_fix":"Examine the v2.3.1 database migration for changes to the task status field or search index. Specifically check: (1) whether a boolean flag like is_archived or is_active was inverted during migration, (2) whether status enum values were remapped incorrectly, (3) whether the search index was rebuilt with inverted status logic. If the search index is separate from the primary data store, a reindex with corrected status mapping may suffice. Also review the search filter query to confirm the predicate matches the intended semantics (e.g., WHERE is_archived = false for active tasks).","proposed_test_case":"Create tasks in both active and archived states. Perform a search with the 'Active tasks only' filter and assert that only active tasks appear. Repeat with the 'Archived tasks' filter and assert only archived tasks appear. Run this test both before and after the v2.3.1 migration to verify correctness.","information_gaps":["Exact details of what the v2.3.1 database migration changed (schema diff)","Whether the search uses a separate index (e.g., Elasticsearch) or queries the primary database directly","Whether the issue affects all teams/organizations or only this team's data partition"]}

---
