# Conversation: wrong-search-results x omo-prometheus

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — that's frustrating, especially when you can see the task exists but search won't surface it.

To help me scope this: **was search working correctly for you before, and if so, when did you first notice it returning wrong results?** Specifically, did anything change around that time — a product update, a change to your workspace settings, or a bulk archive operation?

Also, when you say the archived 'Q2 planning' task appears in results, are you using the default search or do you have any filters applied?

---

### reporter

Yes, search was definitely working fine before. I first noticed the problem about three days ago, right after our system got updated to v2.3.1. I remember because our admin mentioned a database migration was running as part of the patch, and search started acting weird right after that.

Regarding filters — I'm just using the default search. I haven't changed any filter settings. As far as I know, the default is supposed to show active tasks only, which is what I want. But instead it seems like I'm seeing archived tasks in my results. I have about 300 archived tasks and only 150 active ones, and the search results are overwhelmingly showing me old stuff.

What's strange is that if I go directly to a task (not through search), it shows the correct status — like my active tasks still say 'active' when I open them. It's only the search results that seem mixed up.

---

### triage-agent

Thanks — that timeline with v2.3.1 and the database migration is very helpful. The fact that tasks show the correct status when you open them directly, but search returns the wrong mix, strongly suggests the search index is out of sync with the database.

To narrow this down further: **if you create a brand-new task right now, does it show up in search immediately?** And conversely, if you archive a task right now, does it disappear from search results, or does it linger?

This will help me understand whether the search indexing is broken going forward or whether it's specifically the pre-migration data that's stale.

---

### reporter

Good question. I just tried this — I created a test task called 'triage test 123' and searched for it. It did NOT show up in search results. Then I archived it, and oddly enough, it DID show up in search after that. So it seems like the problem isn't just stale data — it's like the search filter is completely inverted. Active tasks are being treated as archived and archived tasks are being treated as active. That would explain why I'm seeing mostly my 300 archived tasks instead of my 150 active ones when I search.

---

### triage-agent

[RESOLVED] {"title":"Search index filter inverted after v2.3.1 migration — active tasks hidden, archived tasks shown","problem":"After the v2.3.1 update and its associated database migration, the default search returns archived tasks instead of active ones. Active tasks are excluded from results. The underlying database is correct (tasks show the right status when viewed directly), so the issue is isolated to the search index layer.","root_cause_hypothesis":"The v2.3.1 database migration likely flipped or incorrectly mapped the active/archived status flag in the search index. The search filter that should include active tasks and exclude archived ones is operating on the inverted value — treating 'active' as 'archived' and vice versa. This could be a boolean inversion, a swapped enum mapping, or a migration that wrote the negated value into the index.","reproduction_steps":["Update to v2.3.1 and allow the database migration to complete","Perform a default search (no filters) for any known active task — it will not appear","Search for a known archived task — it will appear in results","Create a new task, search for it — it will not appear","Archive that task, search again — it will now appear"],"environment":"TaskFlow v2.3.1, post-database migration. Reproduced by multiple users (reporter and teammate). Reporter has ~300 archived tasks and ~150 active tasks.","severity":"critical","impact":"All users are affected. Default search is functionally broken — it surfaces irrelevant archived tasks and hides the active tasks users are actually looking for. Search is a core workflow; this makes the product unreliable for day-to-day use. No workaround exists short of browsing tasks directly without search.","recommended_fix":"Investigate the v2.3.1 database migration script for how it writes the active/archived status to the search index. Look for a boolean inversion, swapped enum values, or an inverted WHERE clause in the index-building query. Fix the mapping and re-index all tasks. Verify by confirming active tasks appear and archived tasks are excluded in default search. Also consider adding an automated check or integration test that validates search index consistency after migrations.","proposed_test_case":"After fix: create an active task and an archived task. Default search should return the active task and exclude the archived one. Additionally, run a consistency check comparing task status in the database against the search index for all tasks — they should match exactly.","information_gaps":["Exact migration script contents (developer would inspect this in code)","Whether the search index uses a separate data store (e.g., Elasticsearch) or queries the DB directly","Whether any other v2.3.1 features besides search are affected by the same inversion"]}

---
