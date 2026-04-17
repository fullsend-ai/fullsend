# Conversation: wrong-search-results x superpowers-brainstorming

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this! To make sure I understand the core issue: which part is causing you more trouble right now?

1. **Archived tasks appearing** in results (cluttering your search)
2. **Active tasks missing** from results (can't find what you need)
3. **Both equally** — search feels fundamentally broken

Also, when you say the 'Q2 planning' active task was created yesterday — can you still find it by browsing your task list directly, just not via search?

---

### reporter

It's both equally — search feels fundamentally broken. When I search, I'm mostly seeing archived tasks instead of active ones, and my active tasks don't show up. It's like the filter is flipped somehow. And yes, I can find the 'Q2 planning' task just fine by browsing my task list directly. It shows up as active, which it should be. It's only when I use search that things go wrong.

---

### triage-agent

That's a really helpful description — "like the filter is flipped" paints a clear picture. One more thing that would help the team track this down: do you know roughly when this started?

1. **Recently** — it was working fine until sometime in the last few days/week
2. **After a specific event** — e.g., you noticed it right after a UI change, an update notification, etc.
3. **Not sure** — you just noticed it now but can't say when it started

This helps the team narrow down which change may have introduced the bug.

---

### reporter

It started recently — I think it was about three days ago. I remember because we had a system update to v2.3.1 around that time, and I'm pretty sure search was working fine before that. I didn't connect the two right away, but now that you ask, the timing lines up.

---

### triage-agent

[RESOLVED] {"title":"Search returns archived tasks and hides active tasks (regression in v2.3.1)","problem":"After the v2.3.1 update (~3 days ago), the search function returns archived tasks that should be hidden and fails to return active tasks that exist and are visible via direct browsing. The behavior affects all searches for all users, suggesting the archive/active filter logic in search is inverted.","root_cause_hypothesis":"The v2.3.1 release likely introduced an inversion in the search query's archive filter — the predicate that should exclude archived tasks is instead selecting only archived tasks (or equivalently, the active-status flag is being negated). This would explain both symptoms simultaneously: archived items appearing and active items disappearing.","reproduction_steps":["Log in to TaskFlow on v2.3.1","Create or confirm an active task exists (e.g., 'Q2 planning')","Verify the task is visible by browsing the task list directly","Use the search function to search for that task's name","Observe that the active task does not appear in search results","Observe that archived tasks matching the query appear instead"],"environment":"TaskFlow v2.3.1 (regression not present in prior version)","severity":"high","impact":"All users are affected. Search is effectively unusable — it returns the opposite of what users expect. Users must fall back to manual browsing to find tasks, which does not scale.","recommended_fix":"Diff the search query/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean or negated predicate on the archived/active status field in the search index query. Likely a one-character fix (e.g., `!is_archived` vs `is_archived`, or a flipped enum comparison). Also check whether the search index itself needs to be rebuilt if the indexing pipeline was affected.","proposed_test_case":"Create one active task and one archived task with the same keyword. Execute a search for that keyword. Assert that the active task appears in results and the archived task does not. Add this as a regression test gated on the archive filter logic.","information_gaps":["Whether the search index itself is corrupted or just the query filter is inverted (determines if a reindex is needed after the fix)","Exact v2.3.1 changelog entry that may have touched search or archive logic"]}

---
