# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds frustrating, especially when you're trying to find something you just created.

To help me understand the full picture: what were you trying to accomplish when you ran that search? Were you looking up the Q2 planning task to update it, share it with someone, or something else? And has search been working correctly for you up until now, or is this the first time you've really relied on it? If it did work before, do you have a sense of when it stopped — did anything change recently (new update, workspace changes, bulk archiving, etc.)?

---

### reporter

I was just trying to find the Q2 planning task to update it with some new deadlines — pretty routine stuff. Search had been working fine for me before this. It started going wrong about three days ago, right after our instance was updated to v2.3.1. I remember getting a notification about a database migration running as part of that patch. Ever since then, search has been returning the wrong tasks. It's like the archive filter is backwards — when I search with the default 'active tasks only' filter, I'm seeing archived tasks instead of my actual active ones. If I flip the filter to show archived tasks, I actually see my active ones there. It's really confusing. The tasks themselves look correct when I open them individually — the archive status is right on the task page. It's just the search filtering that's messed up.

---

### triage-agent

[RESOLVED] {"title":"Search archive filter inverted after v2.3.1 migration — active filter returns archived tasks and vice versa","problem":"Since the v2.3.1 update (approximately three days ago), the search feature's archive filter is inverted. Searching with the default 'active tasks only' filter returns archived tasks, while switching to the 'archived' filter shows active tasks. The underlying task data is correct — individual task pages display the correct archive status. The issue is isolated to how search interprets or applies the archive flag.","root_cause_hypothesis":"The database migration included in v2.3.1 likely inverted the boolean value or enum representing archive status in the search index (e.g., flipped a 0/1 flag, negated an 'is_archived' column, or rebuilt the search index with inverted filter logic). Since the task records themselves are correct but search filtering is backwards, the problem is almost certainly in the search index or the query that filters on archive status — not in the task data itself.","reproduction_steps":["Create a new task with a distinctive name (e.g., 'triage test active') and leave it active","Archive a different task with a distinctive name (e.g., 'triage test archived')","Search for 'triage test' with the default 'active tasks only' filter","Observe that the archived task appears and the active one does not","Switch the filter to 'archived tasks' and observe that the active task now appears"],"environment":"TaskFlow v2.3.1 (upgraded approximately 3 days ago, including a database migration). Affects multiple users in the same instance.","severity":"high","impact":"All users on the instance are affected. Every search with archive filtering returns inverted results, making search effectively unusable for finding active work. Users must manually reverse the filter as a workaround, which is confusing and error-prone.","recommended_fix":"Examine the v2.3.1 database migration script for changes to the archive status field in the search index or search-related tables. Check whether a boolean was flipped, a column default changed, or a reindexing step inverted the flag. The fix is likely either: (a) a corrective migration that re-inverts the flag in the search index, or (b) a fix to the search query logic if the migration changed the semantics of the field (e.g., from 'is_active' to 'is_archived') without updating the query. Since the source-of-truth task data is correct, a search reindex against the correct task archive status should also resolve it.","proposed_test_case":"Create one active and one archived task. Assert that searching with the 'active only' filter returns only the active task, and searching with the 'archived' filter returns only the archived task. Run this test against both pre- and post-migration states to catch regressions.","information_gaps":["Exact search technology in use (database full-text search, Elasticsearch, etc.) — discoverable from codebase","Exact contents of the v2.3.1 migration script — discoverable from version control","Whether any other filter types (e.g., by project, assignee) are also affected — low likelihood given the symptom pattern"]}

---
