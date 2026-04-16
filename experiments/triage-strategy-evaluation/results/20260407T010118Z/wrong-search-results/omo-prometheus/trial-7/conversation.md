# Conversation: wrong-search-results x omo-prometheus

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — that's definitely not expected behavior.

To help us narrow this down: **when did you first notice this?** Was search working correctly before and then broke (and if so, do you remember roughly when), or is this the first time you've relied on search heavily enough to notice? Also, did anything change around that time — a product update, changes to how your team uses archiving, or anything else you can think of?

---

### reporter

Yes, search was definitely working fine before. I first noticed it about three days ago — right after the system was updated to v2.3.1. I remember because we got a notification about a maintenance window for a database migration, and the search issues started right after that. Before the update, searching for tasks worked perfectly and I never saw archived tasks showing up in my results. So the timing lines up exactly with that v2.3.1 patch.

---

### triage-agent

Thanks — that timing with v2.3.1 and the database migration is really helpful and gives us a strong lead.

One important thing to check: **when you look at your tasks outside of search** (e.g. browsing your task list or project board directly), does everything look correct there? Specifically:

- Does the 'Q2 planning' task you created yesterday show up in your normal task list?
- Does the archived one from last year stay hidden in non-search views?

This will help us figure out whether the migration affected the search index specifically, or whether it changed the underlying task data itself — those would be very different problems to fix.

---

### reporter

Good question — yes, everything looks correct outside of search. When I browse my task list or project board directly, the 'Q2 planning' task I created yesterday shows up fine and is marked as active. And the old archived tasks stay hidden like they should. It's only when I use search that things get mixed up. So the actual task data seems fine — it's just the search results that are wrong. In fact, I noticed that if I search and then click into a result, the task detail page shows the correct status (active or archived), but the search itself is returning the wrong set of tasks based on the filter.

---

### triage-agent

That confirms the issue is isolated to the search index — really useful, thank you.

I have a follow-up that will help us zero in on the root cause: **does the search interface have any filter or toggle for task status (e.g., "Active only," "Include archived," etc.)?** If so, what happens when you change it? Specifically:

- If you explicitly filter to "Active only," do the archived tasks still appear?
- If you toggle to "Include archived," does the result set change at all, or does it look the same regardless?

This will tell us whether the search index has stale data (a reindexing problem from the migration) or whether the filter logic itself broke in v2.3.1 — those would point to very different fixes.

---

### reporter

Yes, there is a filter for task status in the search interface. By default it's set to 'Active only,' which is what I normally use. Here's the weird part: when I have it set to 'Active only,' I'm seeing all my archived tasks instead — I have about 300 archived tasks and they're the ones showing up. But when I switch the filter to 'Archived' or 'Include archived,' I actually start seeing my active tasks mixed in. It's like the filter is working backwards — 'Active only' shows archived tasks and the archived filter shows active ones. I tested this a few times to make sure I wasn't imagining it, and it's consistent. So it's not that the search data is missing, it's more like the active/archived flag is flipped in the search results.

---

### triage-agent

[RESOLVED] {"title":"Search status filter inverted after v2.3.1 migration — 'Active only' returns archived tasks and vice versa","problem":"After the v2.3.1 update and its associated database migration, the search status filter operates in reverse. Selecting 'Active only' returns archived tasks (~300 in reporter's case), while selecting 'Archived' or 'Include archived' surfaces active tasks. The underlying task data is correct — browsing task lists and project boards shows the right statuses. The defect is isolated to the search index.","root_cause_hypothesis":"The v2.3.1 database migration inverted the boolean or enum value representing active/archived status when rebuilding or updating the search index. Most likely scenario: a schema change flipped the semantics of the flag (e.g., 'is_active' was replaced with 'is_archived' or a boolean was negated) but the search indexing or query logic was not updated to match, causing the filter predicate to select the opposite set of documents.","reproduction_steps":["Log in to any account that has both active and archived tasks","Navigate to the search interface","Search for any term (e.g., 'Q2 planning') with the default 'Active only' filter","Observe that archived tasks appear in results while known active tasks are missing","Switch the filter to 'Archived' or 'Include archived'","Observe that active tasks now appear in results — the filter is inverted"],"environment":"TaskFlow v2.3.1 (post-database migration). Affects multiple users — reporter and at least one teammate confirmed. Likely affects all users org-wide.","severity":"high","impact":"All users relying on search see inverted results. Active tasks are effectively hidden from search, and archived tasks pollute default results. This undermines core search functionality and could cause users to miss or duplicate work. Workaround exists: users can manually select the opposite filter to get correct results, but this is counterintuitive and unreliable.","recommended_fix":"1. Inspect the v2.3.1 database migration script for changes to the task status field (look for boolean inversions, renamed columns like is_active → is_archived, or changed enum values). 2. Check the search index mapping and query-time filter logic to see which value represents 'active' vs 'archived'. 3. Fix the mismatch — either correct the indexed values via a reindex, or update the query filter predicate to match the new schema semantics. 4. Trigger a full reindex of task status flags after the fix to ensure consistency.","proposed_test_case":"Create an active task and an archived task. Perform a search with 'Active only' filter and assert the active task is returned and the archived task is not. Repeat with 'Archived' filter and assert the inverse. Run this test against both the old and new schema to catch regressions.","information_gaps":["Exact search engine technology (Elasticsearch, PostgreSQL full-text, etc.) — discoverable from codebase","Specific migration script contents and which column/field changed — discoverable from v2.3.1 changelog or migration files","Whether any other indexed fields beyond active/archived status were also affected by the migration"]}

---
